package cli

import (
	"fmt"
	"strings"
)

type commandError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	APICode    any    `json:"apiCode,omitempty"`
	Details    any    `json:"details,omitempty"`
	exit       int
}

func (e *commandError) Error() string { return e.Message }

func failure(code, message string) *commandError {
	return &commandError{Code: code, Message: message, exit: 1}
}

func invalid(message string) *commandError {
	e := failure("INVALID_ARGUMENT", message)
	e.exit = 2
	return e
}

func storageError() error {
	return failure("CREDENTIAL_STORE_UNAVAILABLE", "Cannot access credential storage.")
}

func unsupportedNetwork(id string) error {
	message := fmt.Sprintf("Unsupported network: %s", id)
	if near := nearbyNetworks(id); len(near) > 0 {
		message += ". Did you mean " + strings.Join(near, ", ") + "?"
	} else {
		message += ". List them with nodit network list."
	}
	e := failure("UNSUPPORTED_NETWORK", message)
	e.exit = 2
	return e
}
