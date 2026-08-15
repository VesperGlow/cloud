package storage

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/VesperGlow/cloud/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type CompletedPart struct {
	Number int32  `json:"part_number"`
	ETag   string `json:"etag"`
}
type ObjectInfo struct {
	Size int64
	ETag string
}

type Storage interface {
	Ping(context.Context) error
	PresignPut(context.Context, string, string, time.Duration) (string, error)
	PresignGet(context.Context, string, string, bool, time.Duration) (string, error)
	Head(context.Context, string) (ObjectInfo, error)
	Delete(context.Context, string) error
	CreateMultipart(context.Context, string, string) (string, error)
	PresignPart(context.Context, string, string, int32, time.Duration) (string, error)
	CompleteMultipart(context.Context, string, string, []CompletedPart) error
	AbortMultipart(context.Context, string, string) error
}

type S3 struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
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
	return &S3{client: client, presign: s3.NewPresignClient(presignClient), bucket: c.S3Bucket}, nil
}

func (s *S3) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return err
}
func (s *S3) PresignPut(ctx context.Context, key, mime string, expiry time.Duration) (string, error) {
	in := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
	if mime != "" {
		in.ContentType = aws.String(mime)
	}
	out, err := s.presign.PresignPutObject(ctx, in, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return out.URL, nil
}
func (s *S3) PresignGet(ctx context.Context, key, filename string, inline bool, expiry time.Duration) (string, error) {
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	disposition += "; filename*=UTF-8''" + strings.ReplaceAll(url.PathEscape(filename), "+", "%20")
	out, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ResponseContentDisposition: aws.String(disposition)}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return out.URL, nil
}
func (s *S3) Head(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Size: aws.ToInt64(out.ContentLength), ETag: aws.ToString(out.ETag)}, nil
}
func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}
func (s *S3) CreateMultipart(ctx context.Context, key, mime string) (string, error) {
	in := &s3.CreateMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
	if mime != "" {
		in.ContentType = aws.String(mime)
	}
	out, err := s.client.CreateMultipartUpload(ctx, in)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.UploadId), nil
}
func (s *S3) PresignPart(ctx context.Context, key, uploadID string, part int32, expiry time.Duration) (string, error) {
	out, err := s.presign.PresignUploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(part)}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return out.URL, nil
}
func (s *S3) CompleteMultipart(ctx context.Context, key, uploadID string, parts []CompletedPart) error {
	completed := make([]types.CompletedPart, len(parts))
	for i, part := range parts {
		completed[i] = types.CompletedPart{PartNumber: aws.Int32(part.Number), ETag: aws.String(part.ETag)}
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID), MultipartUpload: &types.CompletedMultipartUpload{Parts: completed}})
	return err
}
func (s *S3) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	return err
}
