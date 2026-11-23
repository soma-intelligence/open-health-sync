// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/soma-intelligence/open-health-sync/internal/prompts"
)

// addPrompts registers the prompt library (internal/prompts). Each prompt
// takes its blanks as arguments plus an optional user_id, and comes back as
// one user message: the question, then the rules every answer holds to.
func addPrompts(s *sdk.Server) {
	for _, p := range prompts.Library {
		args := make([]*sdk.PromptArgument, 0, len(p.Args)+1)
		for _, a := range p.Args {
			desc := a.Description
			if !a.Required() {
				desc += " (default " + a.Default + ")"
			}
			args = append(args, &sdk.PromptArgument{Name: a.Name, Description: desc, Required: a.Required()})
		}
		args = append(args, &sdk.PromptArgument{Name: "user_id",
			Description: "the user whose data to read, with a tenant API key; omit it with a user token"})
		s.AddPrompt(&sdk.Prompt{Name: p.Name, Title: p.Title, Description: p.When, Arguments: args},
			func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
				text, err := PromptText(p, req.Params.Arguments)
				if err != nil {
					// A missing or malformed argument: invalid params, not
					// the generic code 0 a plain error gets.
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
				}
				return &sdk.GetPromptResult{Description: p.When, Messages: []*sdk.PromptMessage{
					{Role: "user", Content: &sdk.TextContent{Text: text}},
				}}, nil
			})
	}
}

// PromptText is the message a prompt returns for args: the filled question,
// the user to read when user_id is set, and the rules.
func PromptText(p prompts.Prompt, args map[string]string) (string, error) {
	q, err := p.Fill(args)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(q)
	if u := strings.TrimSpace(args["user_id"]); u != "" {
		b.WriteString("\n\nRead the data of user_id " + u + ".")
	}
	b.WriteString("\n\nHow to answer:\n")
	for _, r := range prompts.Rules {
		b.WriteString("- " + r + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
