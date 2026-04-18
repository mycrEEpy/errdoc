package testdata

import (
	"fmt"
	"io"
	"os"
)

type MyError struct {
	Code int
}

func (e *MyError) Error() string {
	return fmt.Sprintf("error code: %d", e.Code)
}

type ValidationError struct {
	Field string
}

func (e ValidationError) Error() string {
	return "invalid: " + e.Field
}

func directReturn() error {
	return &MyError{Code: 42}
}

func callsOther() error {
	f, err := os.Open("test.txt")
	if err != nil {
		return err
	}
	_ = f
	return nil
}

func multipleErrors() error {
	_, err := io.ReadAll(nil)
	if err != nil {
		return err
	}
	return ValidationError{Field: "name"}
}

func noErrors() {
	fmt.Println("hello")
}
