package include

type DescStruct struct {
	A uint32
	B uint32
}

var PgDir [1024]uint32

const (
	GDT_NUL  = 0
	GDT_CODE = 1
	GDT_DATA = 2
	GDT_TMP  = 3

	LDT_NUL  = 0
	LDT_CODE = 1
	LDT_DATA = 2
)
