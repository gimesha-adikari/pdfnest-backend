package uploads

import (
	"fmt"
	"mime/multipart"

	"github.com/gofiber/fiber/v2"
)

const LocalKey = "upload_context"

type File struct {
	Field  string
	Header *multipart.FileHeader
	Path   string
}

type Context struct {
	Files map[string][]*File
}

func NewContext() *Context {
	return &Context{
		Files: make(map[string][]*File),
	}
}

func (u *Context) Add(field string, fh *multipart.FileHeader, path string) {
	if u.Files == nil {
		u.Files = make(map[string][]*File)
	}
	u.Files[field] = append(u.Files[field], &File{
		Field:  field,
		Header: fh,
		Path:   path,
	})
}

func (u *Context) First(field string) (*File, bool) {
	if u == nil {
		return nil, false
	}
	files := u.Files[field]
	if len(files) == 0 || files[0] == nil {
		return nil, false
	}
	return files[0], true
}

func (u *Context) All(field string) []*File {
	if u == nil {
		return nil
	}
	return u.Files[field]
}

func (u *Context) Paths(field string) []string {
	files := u.All(field)
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f != nil && f.Path != "" {
			out = append(out, f.Path)
		}
	}
	return out
}

func FromCtx(c *fiber.Ctx) *Context {
	v := c.Locals(LocalKey)
	if ctx, ok := v.(*Context); ok && ctx != nil {
		return ctx
	}
	return nil
}

func MustFile(c *fiber.Ctx, field string) (*File, error) {
	ctx := FromCtx(c)
	if ctx == nil {
		return nil, fmt.Errorf("upload context missing")
	}
	file, ok := ctx.First(field)
	if !ok {
		return nil, fmt.Errorf("missing upload file %q", field)
	}
	return file, nil
}

func MustFiles(c *fiber.Ctx, field string) ([]*File, error) {
	ctx := FromCtx(c)
	if ctx == nil {
		return nil, fmt.Errorf("upload context missing")
	}
	files := ctx.All(field)
	if len(files) == 0 {
		return nil, fmt.Errorf("missing upload files %q", field)
	}
	return files, nil
}
