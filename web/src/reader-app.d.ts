export const ReaderApp: {
  init(root: Document | Element): void
  openBook(bookId: string, options?: {
    embedded?: boolean
    standalone?: boolean
    book?: { id: string; title: string; kind: string }
    onExit?: () => void
  }): Promise<void>
  show(): void
  hide(): void
  saveProgress(): void
  destroyBook(bookId: string): void
  isTocOpen(): boolean
  closeToc(): void
  getState(): { bookId: string; page: number } | null
  restoreState(state: { bookId: string; page?: number } | null): Promise<void>
}
