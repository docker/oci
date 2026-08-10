package ociserver

import (
	"fmt"
	"net/http"
)

// OCIError is a struct that implements the error interface, and formats errors in a way that adheres
// to the OCI distribution spec
type OCIError struct {
	status  int
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Error implements the error interface
func (e *OCIError) Error() string {
	return fmt.Sprintf("%s-%s", e.Code, e.Message)
}

// Status returns the http status code associated with the error
func (e *OCIError) Status() int {
	return e.status
}

// ErrBlobUnknown is for when a blob is not found in the registry
func ErrBlobUnknown(digest string) *OCIError {
	return &OCIError{
		status:  http.StatusNotFound,
		Code:    "BLOB_UNKNOWN",
		Message: fmt.Sprintf("blob (%s) unknown to registry", digest),
	}
}

// ErrBlobUploadInvalid is a generic error message if something is invalid on upload
func ErrBlobUploadInvalid(msg string) *OCIError {
	return &OCIError{
		status:  http.StatusBadRequest,
		Code:    "BLOB_UPLOAD_INVALID",
		Message: fmt.Sprintf("blob upload invalid: %s", msg),
	}
}

// ErrBlobUploadOutOfOrder is an error when blobs are uploaded out of order
func ErrBlobUploadOutOfOrder() *OCIError {
	return &OCIError{
		status:  http.StatusRequestedRangeNotSatisfiable,
		Code:    "BLOB_UPLOAD_INVALID",
		Message: "upload out of order",
	}
}

// ErrBlobUploadUnknown is an error for when the upload session is not found
func ErrBlobUploadUnknown(session string) *OCIError {
	return &OCIError{
		status:  http.StatusNotFound,
		Code:    "BLOB_UPLOAD_UNKNOWN",
		Message: fmt.Sprintf("blob upload (%s) unknown to registry", session),
	}
}

// ErrDigestInvalid is sent if the digest is invalid
func ErrDigestInvalid(msg string) *OCIError {
	return &OCIError{
		status:  http.StatusBadRequest,
		Code:    "DIGEST_INVALID",
		Message: fmt.Sprintf("digest invalid: %s", msg),
	}
}

// ErrManifestBlobUnknown is an error for when the registry has not received a blob referenced by a manifest
func ErrManifestBlobUnknown(digest string) *OCIError {
	return &OCIError{
		status:  http.StatusNotFound,
		Code:    "MANIFEST_BLOB_UNKNOWN",
		Message: fmt.Sprintf("referenced manifest or blob (%s) unknown to registry", digest),
	}
}

// ErrManifestInvalid is an error for when a manifest is invalid in some way
func ErrManifestInvalid(details string) *OCIError {
	return &OCIError{
		status:  http.StatusBadRequest,
		Code:    "MANIFEST_INVALID",
		Message: fmt.Sprintf("manifest is invalid: %s", details),
	}
}

// ErrManifestUnknown is an error for if a manifest is not found in the registry
func ErrManifestUnknown(digest string) *OCIError {
	return &OCIError{
		status:  http.StatusNotFound,
		Code:    "MANIFEST_UNKNOWN",
		Message: fmt.Sprintf("manifest (%s) unknown to registry", digest),
	}
}

// ErrReferrersUnknown is an error for if there are no referrers for a given digest
func ErrReferrersUnknown(digest string) *OCIError {
	return &OCIError{
		status:  http.StatusNotFound,
		Code:    "MANIFEST_UNKNOWN",
		Message: fmt.Sprintf("referrers (%s) unknown to registry", digest),
	}
}

// ErrMediaTypeUnsupported is an error for when a pushed mediatype is unsupported
func ErrMediaTypeUnsupported(mediaType string) *OCIError {
	return &OCIError{
		status:  http.StatusUnsupportedMediaType,
		Code:    "UNSUPPORTED",
		Message: fmt.Sprintf("unsupported mediatype (%s)", mediaType),
	}
}

// ErrNotAcceptable is returned when a stored representation cannot satisfy the Accept header.
func ErrNotAcceptable(mediaType string) *OCIError {
	return &OCIError{
		status:  http.StatusNotAcceptable,
		Code:    "UNSUPPORTED",
		Message: fmt.Sprintf("requested manifest mediatype is not acceptable (%s)", mediaType),
	}
}

// ErrNameUnknown is returned when the repository name is not known to the registry
func ErrNameUnknown(name string) *OCIError {
	return &OCIError{
		status:  http.StatusNotFound,
		Code:    "NAME_UNKNOWN",
		Message: fmt.Sprintf("repository name not known to registry: %s", name),
	}
}

// ErrNameInvalid is returned when the repository name is invalid.
func ErrNameInvalid(name string) *OCIError {
	return &OCIError{
		status:  http.StatusBadRequest,
		Code:    "NAME_INVALID",
		Message: fmt.Sprintf("repository name is invalid: %s", name),
	}
}

// SIZE_INVALID

// ErrUnauthorized is a generic error for if a request is unauthorized
func ErrUnauthorized() *OCIError {
	return &OCIError{
		status:  http.StatusUnauthorized,
		Code:    "UNAUTHORIZED",
		Message: "authentication is required",
	}
}

// ErrDenied is an error for when access is denied
func ErrDenied(msg string) *OCIError {
	return &OCIError{
		status:  http.StatusForbidden,
		Code:    "DENIED",
		Message: "request access is denied",
		Detail:  msg,
	}
}

// UNSUPPORTED

// ErrTooManyRequests is an error if too many requests have been sent
func ErrTooManyRequests() *OCIError {
	return &OCIError{
		status:  http.StatusTooManyRequests,
		Code:    "TOOMANYREQUESTS",
		Message: "too many requests",
	}
}

// ErrRangeNotSatisfiable is returned when a requested byte range cannot be satisfied.
func ErrRangeNotSatisfiable(msg string) *OCIError {
	return &OCIError{
		status:  http.StatusRequestedRangeNotSatisfiable,
		Code:    "UNSUPPORTED",
		Message: msg,
	}
}

// ErrServerError is returned when an unexpected internal error occurs.
func ErrServerError() *OCIError {
	return &OCIError{
		status:  http.StatusInternalServerError,
		Code:    "SERVER_ERROR",
		Message: "internal server error",
	}
}

// ErrBadRequest is not an OCI error... just added for a catch-all for now
func ErrBadRequest(msg string) *OCIError {
	return &OCIError{
		status:  http.StatusBadRequest,
		Code:    "BAD_REQUEST",
		Message: msg,
	}
}

// ErrMethodNotAllowed is returned when an operation is not permitted on the target resource.
func ErrMethodNotAllowed(msg string) *OCIError {
	return &OCIError{
		status:  http.StatusMethodNotAllowed,
		Code:    "UNSUPPORTED",
		Message: msg,
	}
}

// ErrManifestTooLarge is returned when the pushed manifest exceeds the registry size limit.
func ErrManifestTooLarge() *OCIError {
	return &OCIError{
		status:  http.StatusRequestEntityTooLarge,
		Code:    "MANIFEST_INVALID",
		Message: "manifest exceeds maximum allowed size",
	}
}

// ErrNotImplemented is returned when an endpoint or operation has not been implemented.
func ErrNotImplemented() *OCIError {
	return &OCIError{
		status:  http.StatusNotImplemented,
		Code:    "UNSUPPORTED",
		Message: "operation is not implemented",
	}
}
