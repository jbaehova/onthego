package contextenv

import "github.com/jbaehova/onthego/internal/handoff"

func handoffInput(goal string) handoff.Input {
	return handoff.Input{Goal: goal}
}
