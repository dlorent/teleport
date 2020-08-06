/*
Copyright 2020 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package srv

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/pborman/uuid"
	"gopkg.in/check.v1"
)

type LabelSuite struct {
}

var _ = check.Suite(&LabelSuite{})

func (s *LabelSuite) SetUpSuite(c *check.C) {
	utils.InitLoggerForTests(testing.Verbose())
}

func (s *LabelSuite) TearDownSuite(c *check.C) {}
func (s *LabelSuite) SetUpTest(c *check.C)     {}
func (s *LabelSuite) TearDownTest(c *check.C)  {}

func (s *LabelSuite) TestSync(c *check.C) {
	// Create dynamic labels and sync right away.
	l, err := NewDynamicLabels(&DynamicLabelsConfig{
		Labels: map[string]services.CommandLabel{
			"foo": &services.CommandLabelV2{
				Period:  services.NewDuration(1 * time.Second),
				Command: []string{"expr", "1", "+", "3"}},
		},
		CloseContext: context.Background(),
	})
	c.Assert(err, check.IsNil)
	l.Sync()

	// Check that the result contains the output of the command.
	val, ok := l.Get()["foo"]
	c.Assert(ok, check.Equals, true)
	c.Assert(val.GetResult(), check.Equals, "4")
}

func (s *LabelSuite) TestRun(c *check.C) {
	// Create dynamic labels and setup async update.
	l, err := NewDynamicLabels(&DynamicLabelsConfig{
		Labels: map[string]services.CommandLabel{
			"foo": &services.CommandLabelV2{
				Period:  services.NewDuration(1 * time.Second),
				Command: []string{"expr", "1", "+", "3"}},
		},
		CloseContext: context.Background(),
	})
	c.Assert(err, check.IsNil)
	l.Run()

	// When checked right away, result should be empty. Loop to update dynamic
	// labels has not run yet.
	val, ok := l.Get()["foo"]
	c.Assert(ok, check.Equals, true)
	c.Assert(val.GetResult(), check.Equals, "")

	// Wait a maximum of 2 seconds for dynamic labels to be updated (should be
	// updated at 1 second).
	select {
	case <-time.Tick(250 * time.Millisecond):
		val, ok := l.Get()["foo"]
		c.Assert(ok, check.Equals, true)
		if val.GetResult() == "4" {
			break
		}
	case <-time.After(2 * time.Second):
		c.Fatalf("Timed out waiting for label to be updated.")
	}
}

// TestInvalidCommand makes sure that invalid commands return a error message.
func (s *LabelSuite) TestInvalidCommand(c *check.C) {
	// Create invalid labels and sync right away.
	l, err := NewDynamicLabels(&DynamicLabelsConfig{
		Labels: map[string]services.CommandLabel{
			"foo": &services.CommandLabelV2{
				Period:  services.NewDuration(1 * time.Second),
				Command: []string{uuid.New()}},
		},
		CloseContext: context.Background(),
	})
	c.Assert(err, check.IsNil)
	l.Sync()

	// Check that the output contains that the command was not found.
	val, ok := l.Get()["foo"]
	c.Assert(ok, check.Equals, true)
	c.Assert(strings.Contains(val.GetResult(), "executable file not found"), check.Equals, true)
}
