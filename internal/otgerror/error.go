package otgerror

import "fmt"

const (
	CodeSourceChanged      = "E_SOURCE_CHANGED"
	CodeUnsupportedGit     = "E_UNSUPPORTED_GIT"
	CodeSecretPolicy       = "E_SECRET_POLICY"
	CodePackageInvalid     = "E_PACKAGE_INVALID"
	CodeTargetAuth         = "E_TARGET_AUTH"
	CodeTargetUnavailable  = "E_TARGET_UNAVAILABLE"
	CodeGenerationConflict = "E_GENERATION_CONFLICT"
	CodeRunUncertain       = "E_RUN_UNCERTAIN"
	CodeSessionFormat      = "E_SESSION_FORMAT"
	CodeAgentAuth          = "E_AGENT_AUTH"
	CodeRestoreFailed      = "E_RESTORE_FAILED"
	CodeInput              = "E_INPUT"
	CodePrecondition       = "E_PRECONDITION"
)

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Stage     string `json:"stage,omitempty"`
	Preserved string `json:"preserved_path,omitempty"`
	Cause     error  `json:"-"`
}

func (e *Error) Error() string {
	message := e.Message
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	if e.Stage != "" {
		return fmt.Sprintf("%s at %s: %s", e.Code, e.Stage, message)
	}
	return fmt.Sprintf("%s: %s", e.Code, message)
}

func (e *Error) Unwrap() error { return e.Cause }

func New(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func Wrap(code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}
