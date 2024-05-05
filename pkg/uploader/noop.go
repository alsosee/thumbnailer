package uploader

type NoOp struct{}

func NewNoOp() *NoOp {
	return &NoOp{}
}

func (n *NoOp) Upload(key string, body []byte) error {
	return nil
}
