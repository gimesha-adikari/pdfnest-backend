package pdfdraw

type Color struct {
	R float64
	G float64
	B float64
}

type Rect struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

type Highlight struct {
	Page    int
	Rect    Rect
	Color   Color
	Opacity float64
}

type Text struct {
	Page     int
	X        float64
	Y        float64
	Value    string
	FontName string
	FontSize float64
	Color    Color
	Rotation float64
}

type DrawCommand interface {
	PageNumber() int
}

func (h Highlight) PageNumber() int {
	return h.Page
}

func (t Text) PageNumber() int {
	return t.Page
}
