package repository

import "errors"

// Domain errors.
var (
	ErrRepositoryNotFound        = errors.New("repository not found")
	ErrRepositoryExists          = errors.New("repository already exists")
	ErrRepositoryKeyCaseConflict = errors.New("case insensitive repository key already exists")
	ErrInvalidRepository         = errors.New("invalid repository configuration")
	ErrNotLocal                  = errors.New("repository does not accept deployments")
	ErrNotFound                  = errors.New("item not found")
	ErrInvalidOffset             = errors.New("negative read offset")
	ErrConflict                  = errors.New("path conflicts with an existing item")
	ErrChecksumMismatch          = errors.New("checksum mismatch")
	ErrChecksumUnknown           = errors.New("no content with this checksum")
	ErrNotAFile                  = errors.New("item is a folder")
	ErrForbidden                 = errors.New("permission denied")
	ErrIncompleteUpload          = errors.New("object storage did not keep the whole upload")
	ErrContentMissing            = errors.New("stored content is missing from object storage")
	ErrContentCorrupt            = errors.New("stored content does not match its checksum")
	ErrTooLarge                  = errors.New("the upload is larger than the repository accepts")
	ErrHeld                      = errors.New("the item is under a legal hold")
	ErrImmutable                 = errors.New("the repository does not allow overwriting a file")
	ErrQuotaExceeded             = errors.New("the repository's storage quota is used up")
)
