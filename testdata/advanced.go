package testdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
)

// PermissionError is a concrete error type with a pointer receiver.
type PermissionError struct {
	User string
}

func (e *PermissionError) Error() string {
	return e.User + ": permission denied"
}

// Service demonstrates method receivers returning typed errors.
type Service struct{}

// Fetch is a method that returns a typed error directly.
func (s *Service) Fetch() error {
	return &PermissionError{User: "admin"}
}

// Process is a method that calls another method on the same type.
func (s *Service) Process() error {
	return s.Fetch()
}

// chainedCalls calls a function that itself calls os.Open transitively.
func chainedCalls() error {
	return readConfig()
}

func readConfig() error {
	_, err := os.ReadFile("config.yaml")
	return err
}

// multiReturn demonstrates a function with multiple return values.
func multiReturn() (int, error) {
	n, err := strconv.Atoi("abc")
	if err != nil {
		return 0, err
	}
	return n, nil
}

// usesErrorsNew uses errors.New which returns *errors.errorString.
func usesErrorsNew() error {
	return errors.New("something failed")
}

// usesFmtErrorf uses fmt.Errorf which returns *fmt.wrapError.
func usesFmtErrorf() error {
	return fmt.Errorf("wrap: %w", errors.New("inner"))
}

// netDial calls net.Dial which returns concrete net error types.
func netDial() error {
	_, err := net.Dial("tcp", "localhost:9999")
	return err
}

// jsonDecode calls json.Unmarshal which can return typed errors.
func jsonDecode() error {
	return json.Unmarshal([]byte("{}"), &struct{}{})
}

// multipleCallSites combines errors from several different call sites.
func multipleCallSites() error {
	if _, err := os.Open("a"); err != nil {
		return err
	}
	if _, err := strconv.Atoi("b"); err != nil {
		return err
	}
	return nil
}

// nilOnly always returns nil.
func nilOnly() error {
	return nil
}
