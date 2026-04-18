# errdoc

`errdoc` analyzes a Go source file and reports all concrete error types
returned by each function, including errors returned transitively by
called functions across package boundaries.
