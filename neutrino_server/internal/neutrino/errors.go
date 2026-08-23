package neutrino

import "fmt"

// UnavailableError represents temporarily unavailable chain data.
// This should result in HTTP 503 responses so callers can retry.
type UnavailableError struct {
	Resource string
	Height   int32
	Err      error
}

func (e *UnavailableError) Error() string {
	message := fmt.Sprintf("%s unavailable", e.Resource)
	if e.Height >= 0 {
		message = fmt.Sprintf("%s at height %d", message, e.Height)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", message, e.Err)
	}
	return message
}

func (e *UnavailableError) Unwrap() error {
	return e.Err
}

// NewUnavailableError creates a new UnavailableError.
func NewUnavailableError(resource string, height int32, err error) *UnavailableError {
	return &UnavailableError{
		Resource: resource,
		Height:   height,
		Err:      err,
	}
}

// NotFoundError represents an error when a requested resource is not found.
// This should result in HTTP 404 responses.
type NotFoundError struct {
	Resource string
	Message  string
}

func (e *NotFoundError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("%s not found", e.Resource)
}

// NewNotFoundError creates a new NotFoundError.
func NewNotFoundError(resource string, message string) *NotFoundError {
	return &NotFoundError{
		Resource: resource,
		Message:  message,
	}
}

// BadRequestError represents an error due to invalid client input.
// This should result in HTTP 400 responses.
type BadRequestError struct {
	Message string
}

func (e *BadRequestError) Error() string {
	return e.Message
}

// NewBadRequestError creates a new BadRequestError.
func NewBadRequestError(message string) *BadRequestError {
	return &BadRequestError{Message: message}
}
