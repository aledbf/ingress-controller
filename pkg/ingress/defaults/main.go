package defaults

type Upstream struct {
	Secure               bool
	MaxFails             int
	FailTimeout          int
	ConnectTimeout       int
	SendTimeout          int
	ReadTimeout          int
	BufferSize           string
	WhitelistSourceRange []string
}
