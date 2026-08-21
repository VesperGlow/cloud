package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/VesperGlow/cloud/internal/config"
	"github.com/VesperGlow/cloud/internal/fastcdc"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

var ErrObjectTooLarge = errors.New("object exceeds read limit")

type ObjectInfo struct {
	Size int64
	ETag string
}

// ObjectRef describes one listed object, used by the garbage collector.
type ObjectRef struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Storage is the object storage control plane. File content is stored as
// content-addressed blocks plus a small JSON manifest (the Seafile
// "fs object" analogue); small fixed blobs (avatars) use raw objects.
type Storage interface {
	Ping(context.Context) error

	// Blocks: variable-size content-addressed chunks under blocks/xx/<sha256>.
	PresignBlockPut(context.Context, string, time.Duration) (string, error)
	HeadBlock(context.Context, string) (Block, error)
	GetBlock(context.Context, string) ([]byte, error)
	ListBlocks(context.Context) ([]ObjectRef, error)

	// Manifests: JSON block lists under manifests/xx/<sha256-of-json>.
	PutManifest(context.Context, Manifest) (string, error)
	GetManifest(context.Context, string) (Manifest, error)
	ListManifests(context.Context) ([]ObjectRef, error)

	// Server-side whole-content write (documents, legacy migration).
	Store(context.Context, io.Reader) (string, Manifest, error)

	// Reading a logical file back as a stream.
	Open(context.Context, string) (io.ReadSeekCloser, error)
	ReadFile(context.Context, string, int64) ([]byte, error)

	// Raw single objects (avatar, legacy objects, GC cleanup).
	PutObject(context.Context, string, string, []byte) (ObjectInfo, error)
	OpenRaw(context.Context, string) (io.ReadCloser, error)
	GetObject(context.Context, string, int64) ([]byte, error)
	DeleteObject(context.Context, string) error
	PresignGetObject(context.Context, string, string, string, bool, time.Duration) (string, error)
	ListPrefix(context.Context, string) ([]ObjectRef, error)
	// PutImmutable stores a content-addressed derived object (thumbnail
	// cache); an existing object is never overwritten.
	PutImmutable(context.Context, string, string, []byte) error
}

type S3 struct {
	client       *s3.Client
	presign      *s3.PresignClient
	bucket       string
	maxBlockSize int64
	chunking     fastcdc.Config
}

func NewS3(ctx context.Context, c config.Config) (*S3, error) {
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(c.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.S3AccessKey, c.S3SecretKey, "")),
	}
	if c.S3Endpoint != "" {
		options = append(options, awsconfig.WithBaseEndpoint(c.S3Endpoint))
	}
	awscfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	client := s3.NewFromConfig(awscfg, func(o *s3.Options) { o.UsePathStyle = c.S3PathStyle })
	presignConfig := awscfg
	if c.S3PublicEndpoint != "" {
		presignConfig.BaseEndpoint = aws.String(c.S3PublicEndpoint)
	}
	presignClient := s3.NewFromConfig(presignConfig, func(o *s3.Options) {
		o.UsePathStyle = c.S3PathStyle
		if c.S3PublicEndpoint != "" {
			o.BaseEndpoint = aws.String(c.S3PublicEndpoint)
		}
	})
	minimum, average, maximum := c.ChunkSizes()
	chunking, err := fastcdc.NewConfig(int(minimum), int(average), int(maximum))
	if err != nil {
		return nil, fmt.Errorf("configure FastCDC: %w", err)
	}
	return &S3{client: client, presign: s3.NewPresignClient(presignClient), bucket: c.S3Bucket, maxBlockSize: maximum, chunking: chunking}, nil
}

func (s *S3) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return err
}

// PresignBlockPut issues a conditional PUT URL for one block. The URL is
// bound to If-None-Match: *, so an existing content-addressed block can
// never be overwritten and concurrent identical uploads race harmlessly
// (the loser receives 412 Precondition Failed).
func (s *S3) PresignBlockPut(ctx context.Context, id string, expiry time.Duration) (string, error) {
	in := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(BlockKey(id)),
		ContentType: aws.String("application/octet-stream"),
		IfNoneMatch: aws.String("*"),
	}
	out, err := s.presign.PresignPutObject(ctx, in, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return out.URL, nil
}

func (s *S3) HeadBlock(ctx context.Context, id string) (Block, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(BlockKey(id))})
	if err != nil {
		return Block{}, err
	}
	return Block{ID: id, Size: aws.ToInt64(out.ContentLength)}, nil
}

func (s *S3) GetBlock(ctx context.Context, id string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(BlockKey(id))})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, s.maxBlockSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.maxBlockSize {
		return nil, fmt.Errorf("block %s exceeds configured block size", id)
	}
	return data, nil
}

// putConditional stores an immutable content-addressed object. A concurrent
// upload of identical content loses the race with 412, which is treated as
// success because the object that exists is identical by construction.
func (s *S3) putConditional(ctx context.Context, key, mime string, data []byte) error {
	in := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		IfNoneMatch:   aws.String("*"),
	}
	if mime != "" {
		in.ContentType = aws.String(mime)
	}
	_, err := s.client.PutObject(ctx, in)
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
		return nil
	}
	return err
}

func (s *S3) ListBlocks(ctx context.Context) ([]ObjectRef, error) {
	return s.ListPrefix(ctx, blockPrefix)
}
func (s *S3) ListManifests(ctx context.Context) ([]ObjectRef, error) {
	return s.ListPrefix(ctx, manifestPrefix)
}

func (s *S3) ListPrefix(ctx context.Context, prefix string) ([]ObjectRef, error) {
	var out []ObjectRef
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			out = append(out, ObjectRef{
				Key:          aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
			})
		}
	}
	return out, nil
}

func (s *S3) PresignGetObject(ctx context.Context, key, filename, mime string, inline bool, expiry time.Duration) (string, error) {
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	disposition += "; filename*=UTF-8''" + strings.ReplaceAll(url.PathEscape(filename), "+", "%20")
	in := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ResponseContentDisposition: aws.String(disposition)}
	if mime != "" {
		in.ResponseContentType = aws.String(mime)
	}
	out, err := s.presign.PresignGetObject(ctx, in, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return out.URL, nil
}

func (s *S3) PutObject(ctx context.Context, key, mime string, data []byte) (ObjectInfo, error) {
	in := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data)))}
	if mime != "" {
		in.ContentType = aws.String(mime)
	}
	out, err := s.client.PutObject(ctx, in)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Size: int64(len(data)), ETag: aws.ToString(out.ETag)}, nil
}

func (s *S3) OpenRaw(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (s *S3) GetObject(ctx context.Context, key string, limit int64) ([]byte, error) {
	body, err := s.OpenRaw(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrObjectTooLarge
	}
	return data, nil
}

// IsNotFound reports whether the error means the S3 object is absent.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}

// PutImmutable stores a content-addressed derived object (thumbnail cache);
// concurrent identical uploads race harmlessly and existing objects are
// never overwritten.
func (s *S3) PutImmutable(ctx context.Context, key, mime string, data []byte) error {
	return s.putConditional(ctx, key, mime, data)
}

// DeleteObject is idempotent: deleting an already-absent key is not an error.
func (s *S3) DeleteObject(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err == nil {
		return nil
	}
	if IsNotFound(err) {
		return nil
	}
	return err
}
