//go:build http && ui

package ui

import (
	"testing"

	"github.com/cucumber/godog"
)

// The menu is state of the rendered composer, so each step runs the Vitest
// test that sends or queues a draft with the menu open.
func TestComposerMenuAfterSendFeature(t *testing.T) {
	const composer = "src/ui/chat/Composer.test.tsx"
	steps := []struct{ step, name string }{
		{`^Send clicked while the slash menu is open closes the menu$`,
			"Send clicked while the slash menu is open closes the menu"},
		{`^a draft queued while the slash menu is open closes the menu$`,
			"a draft queued while the slash menu is open closes the menu"},
	}
	suite := godog.TestSuite{
		Name: "composer_menu_after_send",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			for _, s := range steps {
				name := s.name
				sc.Step(s.step, func() error { return runVitestScenario(composer, name) })
			}
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/composer_menu_after_send.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("composer menu after send feature failed")
	}
}
