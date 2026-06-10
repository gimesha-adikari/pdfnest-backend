package models

import "mime/multipart"

type SecurityFormRequest struct {
	Password string                `form:"password"`
	File     *multipart.FileHeader `form:"file"`
}
