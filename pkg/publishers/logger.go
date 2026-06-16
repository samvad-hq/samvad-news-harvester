package publishers

// Logger defines the logging surface publishers rely on.
type Logger interface {
	InfoObj(msg, key string, obj interface{})
	DebugObj(msg, key string, obj interface{})
	WarnObj(msg, key string, obj interface{})
	ErrorObj(msg, key string, obj interface{})
}

type noopLogger struct{}

func (noopLogger) InfoObj(string, string, interface{})  {}
func (noopLogger) DebugObj(string, string, interface{}) {}
func (noopLogger) WarnObj(string, string, interface{})  {}
func (noopLogger) ErrorObj(string, string, interface{}) {}

func ensureLogger(log Logger) Logger {
	if log == nil {
		return noopLogger{}
	}
	return log
}
