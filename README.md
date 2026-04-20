# errdoc

`errdoc` analyzes a Go source file and reports all concrete error types
returned by each function, including errors returned transitively by
called functions across package boundaries.

## Install

```sh
go install github.com/mycreepy/errdoc/cmd/errdoc@latest
```

## Usage

```
errdoc [-w] [-u] <file.go|directory>
```

| Flag | Description |
|------|-------------|
| `-w` | Write error types into function doc comments |
| `-u` | Include unexported error types in output |

## Examples

Analyze a single file:

```sh
errdoc myfile.go
```

Analyze all files in a directory:

```sh
errdoc ./pkg/server
```

Sample output:

```
func directReturn:
  *github.com/mycreepy/errdoc/testdata.MyError
func callsOther:
  *os.PathError
  syscall.Errno
func multipleErrors:
  github.com/mycreepy/errdoc/testdata.ValidationError
```

Write error types into the source file's doc comments:

```sh
errdoc -w myfile.go
```

This inserts a `Returns errors:` block into each function's doc comment:

```go
// Returns errors:
//
//   *os.PathError
//   syscall.Errno
func callsOther() error {
```

Running with `-w` is idempotent — existing `Returns errors:` blocks are
replaced in place.
