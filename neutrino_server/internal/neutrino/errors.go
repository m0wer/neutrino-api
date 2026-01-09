package neutrino

import "fmt"

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
