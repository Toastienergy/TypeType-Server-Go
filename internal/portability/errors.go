package portability

import "net/http"

// Error is how a portability failure answers, as respondPortabilityError maps the Kotlin exceptions: contract and
// argument errors keep their message, state and missing-job errors do not.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func contract(code, msg string) *Error { return &Error{http.StatusBadRequest, code, msg} }

func invalid(msg string) *Error {
	return &Error{http.StatusBadRequest, "portability_invalid_input", msg}
}

func badState() *Error {
	return &Error{http.StatusConflict, "portability_invalid_state", "Portability operation failed"}
}

var (
	ErrNotFound      = &Error{http.StatusNotFound, "portability_job_not_found", "Portability operation failed"}
	ErrUploadTooBig  = &Error{http.StatusRequestEntityTooLarge, "portability_upload_too_large", "Backup exceeds the upload limit"}
	errUnknownFormat = contract("portability_format_unknown", "Unable to detect the backup format")
)

func unsupportedFormat(f Format) *Error {
	return contract("portability_format_unsupported", "No adapter is registered for "+string(f))
}

func mismatch(f Format) *Error {
	return contract("portability_format_mismatch", "Backup does not match "+string(f))
}

// AsError maps any error to its answer; anything unexpected is a 500 without detail.
func AsError(err error) *Error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{http.StatusInternalServerError, "portability_failed", "Portability operation failed"}
}
