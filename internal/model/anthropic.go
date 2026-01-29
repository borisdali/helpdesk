package model

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/genai"
)

// AnthropicModel implements the adkmodel.LLM interface for Anthropic Claude.
type AnthropicModel struct {
	client    anthropic.Client
	modelName string
}

// NewAnthropicModel creates a new Anthropic model client.
func NewAnthropicModel(ctx context.Context, modelName, apiKey string) (*AnthropicModel, error) {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &AnthropicModel{
		client:    client,
		modelName: modelName,
	}, nil
}

// Name returns the model name.
func (m *AnthropicModel) Name() string {
	return m.modelName
}

// GenerateContent implements the adkmodel.LLM interface.
func (m *AnthropicModel) GenerateContent(ctx context.Context, req *adkmodel.LLMRequest, stream bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		slog.Debug("GenerateContent called", "stream", stream)

		// Convert ADK request to Anthropic format
		anthropicReq, err := m.convertRequest(req)
		if err != nil {
			slog.Error("failed to convert request", "err", err)
			yield(nil, fmt.Errorf("failed to convert request: %w", err))
			return
		}

		// Always use non-streaming for now - streaming has issues with tool calls
		slog.Debug("using non-streaming mode")
		m.generateNonStreaming(ctx, anthropicReq, yield)
	}
}

// convertRequest converts an ADK LLMRequest to Anthropic message params.
func (m *AnthropicModel) convertRequest(req *adkmodel.LLMRequest) (anthropic.MessageNewParams, error) {
	slog.Debug("converting request", "tools_count", len(req.Tools))
	for name, tool := range req.Tools {
		slog.Debug("tool found", "name", name, "type", fmt.Sprintf("%T", tool))
	}
	slog.Debug("converting request contents", "contents_count", len(req.Contents))
	for i, content := range req.Contents {
		slog.Debug("content entry", "index", i, "role", content.Role, "parts", len(content.Parts))
		for j, part := range content.Parts {
			if part.Text != "" {
				// Log first 500 chars of text to see system prompt
				text := part.Text
				if len(text) > 500 {
					text = text[:500] + "..."
				}
				slog.Debug("content part text", "content_index", i, "part_index", j, "text", text)
			}
		}
	}

	// Check for SystemInstruction in Config
	if req.Config != nil && req.Config.SystemInstruction != nil {
		slog.Debug("system instruction found", "role", req.Config.SystemInstruction.Role, "parts", len(req.Config.SystemInstruction.Parts))
		for j, part := range req.Config.SystemInstruction.Parts {
			if part.Text != "" {
				text := part.Text
				if len(text) > 500 {
					text = text[:500] + "..."
				}
				slog.Debug("system instruction part", "part_index", j, "text", text)
			}
		}
	}

	var messages []anthropic.MessageParam
	var systemPrompts []anthropic.TextBlockParam

	// Extract system instruction from Config if present
	if req.Config != nil && req.Config.SystemInstruction != nil {
		for _, part := range req.Config.SystemInstruction.Parts {
			if part.Text != "" {
				systemPrompts = append(systemPrompts, anthropic.TextBlockParam{
					Text: part.Text,
				})
			}
		}
	}

	// Convert contents to Anthropic messages
	for _, content := range req.Contents {
		if content.Role == "system" {
			// Extract system prompt
			for _, part := range content.Parts {
				if part.Text != "" {
					systemPrompts = append(systemPrompts, anthropic.TextBlockParam{
						Text: part.Text,
					})
				}
			}
			continue
		}

		msg, err := m.convertContent(content)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		messages = append(messages, msg)
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(m.modelName),
		Messages:  messages,
		MaxTokens: 4096,
	}

	if len(systemPrompts) > 0 {
		params.System = systemPrompts
	}

	// Convert tools if present
	if len(req.Tools) > 0 {
		tools, err := m.convertTools(req.Tools)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		params.Tools = tools
	}

	// Apply config if present
	if req.Config != nil {
		if req.Config.Temperature != nil {
			params.Temperature = anthropic.Float(float64(*req.Config.Temperature))
		}
		if req.Config.MaxOutputTokens != 0 {
			params.MaxTokens = int64(req.Config.MaxOutputTokens)
		}
		if req.Config.TopP != nil {
			params.TopP = anthropic.Float(float64(*req.Config.TopP))
		}
	}

	return params, nil
}

// convertContent converts a genai.Content to an Anthropic MessageParam.
func (m *AnthropicModel) convertContent(content *genai.Content) (anthropic.MessageParam, error) {
	var blocks []anthropic.ContentBlockParamUnion

	for _, part := range content.Parts {
		switch {
		case part.Text != "":
			blocks = append(blocks, anthropic.NewTextBlock(part.Text))

		case part.FunctionCall != nil:
			// Model requesting a tool call - convert to tool_use block
			blocks = append(blocks, anthropic.NewToolUseBlock(
				part.FunctionCall.ID,
				part.FunctionCall.Args,
				part.FunctionCall.Name,
			))

		case part.FunctionResponse != nil:
			// Tool result being sent back
			resultJSON, err := json.Marshal(part.FunctionResponse.Response)
			if err != nil {
				return anthropic.MessageParam{}, err
			}
			blocks = append(blocks, anthropic.NewToolResultBlock(
				part.FunctionResponse.ID,
				string(resultJSON),
				false, // isError
			))

		case part.InlineData != nil:
			// Binary data (images)
			blocks = append(blocks, anthropic.NewImageBlockBase64(
				part.InlineData.MIMEType,
				string(part.InlineData.Data),
			))
		}
	}

	// Use helper functions based on role
	if content.Role == "model" || content.Role == "assistant" {
		return anthropic.NewAssistantMessage(blocks...), nil
	}
	return anthropic.NewUserMessage(blocks...), nil
}

// toolInterface matches the tool.Tool interface from ADK
type toolInterface interface {
	Name() string
	Description() string
}

// declarationProvider matches tools that have a Declaration method
type declarationProvider interface {
	Declaration() *genai.FunctionDeclaration
}

// convertTools converts ADK tools to Anthropic tool definitions.
func (m *AnthropicModel) convertTools(tools map[string]any) ([]anthropic.ToolUnionParam, error) {
	var result []anthropic.ToolUnionParam

	for name, toolDef := range tools {
		var description string
		var parameters *genai.Schema

		// Handle different tool definition types
		switch def := toolDef.(type) {
		case *genai.FunctionDeclaration:
			description = def.Description
			parameters = def.Parameters
		case genai.FunctionDeclaration:
			description = def.Description
			parameters = def.Parameters
		default:
			// Try tool.Tool interface first (has Description() method)
			if t, ok := toolDef.(toolInterface); ok {
				description = t.Description()
				slog.Debug("tool description from interface", "tool", name, "description", description)
			}

			// Try to get Declaration() for parameters
			if dp, ok := toolDef.(declarationProvider); ok {
				if decl := dp.Declaration(); decl != nil {
					if description == "" {
						description = decl.Description
					}
					parameters = decl.Parameters
					slog.Debug("tool declaration with params", "tool", name)
				}
			}

			// Fallback to JSON marshaling if we still don't have description
			if description == "" {
				toolBytes, err := json.Marshal(toolDef)
				if err != nil {
					slog.Warn("could not marshal tool", "tool", name, "type", fmt.Sprintf("%T", toolDef), "err", err)
					continue
				}
				var toolMap map[string]interface{}
				if err := json.Unmarshal(toolBytes, &toolMap); err != nil {
					slog.Warn("could not unmarshal tool", "tool", name, "err", err)
					continue
				}
				if desc, ok := toolMap["description"].(string); ok {
					description = desc
				}
				if desc, ok := toolMap["Description"].(string); ok {
					description = desc
				}
				// Try to get parameters/schema
				if parameters == nil {
					if params, ok := toolMap["parameters"]; ok {
						paramBytes, _ := json.Marshal(params)
						parameters = &genai.Schema{}
						json.Unmarshal(paramBytes, parameters)
					}
					if params, ok := toolMap["Parameters"]; ok {
						paramBytes, _ := json.Marshal(params)
						parameters = &genai.Schema{}
						json.Unmarshal(paramBytes, parameters)
					}
				}
				slog.Debug("tool used JSON fallback", "tool", name, "type", fmt.Sprintf("%T", toolDef))
			}
		}

		// Convert the schema
		inputSchema := anthropic.ToolInputSchemaParam{
			Type: "object",
		}

		if parameters != nil {
			// Parameters is a *genai.Schema, convert to map
			schemaBytes, err := json.Marshal(parameters)
			if err != nil {
				return nil, err
			}
			var schemaMap map[string]interface{}
			if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
				return nil, err
			}
			if props, ok := schemaMap["properties"]; ok {
				inputSchema.Properties = props
			}
			if required, ok := schemaMap["required"].([]interface{}); ok {
				var reqStrings []string
				for _, r := range required {
					if s, ok := r.(string); ok {
						reqStrings = append(reqStrings, s)
					}
				}
				inputSchema.Required = reqStrings
			}
		}

		slog.Debug("adding tool", "tool", name, "description", description)

		result = append(result, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        name,
				Description: anthropic.String(description),
				InputSchema: inputSchema,
			},
		})
	}

	return result, nil
}

// generateNonStreaming handles non-streaming response.
func (m *AnthropicModel) generateNonStreaming(ctx context.Context, req anthropic.MessageNewParams, yield func(*adkmodel.LLMResponse, error) bool) {
	slog.Debug("making API call")
	resp, err := m.client.Messages.New(ctx, req)
	if err != nil {
		slog.Error("anthropic API error", "err", err)
		yield(nil, fmt.Errorf("anthropic API error: %w", err))
		return
	}

	slog.Debug("API response received", "content_blocks", len(resp.Content), "stop_reason", resp.StopReason)
	for i, block := range resp.Content {
		slog.Debug("response block", "index", i, "type", block.Type)
		if block.Type == "tool_use" {
			slog.Debug("tool_use block", "index", i, "id", block.ID, "name", block.Name)
		}
	}

	llmResp := m.convertResponse(resp)
	slog.Debug("converted response", "parts", len(llmResp.Content.Parts))
	for i, part := range llmResp.Content.Parts {
		if part.FunctionCall != nil {
			slog.Debug("response part function_call", "index", i, "name", part.FunctionCall.Name, "id", part.FunctionCall.ID, "args", part.FunctionCall.Args)
		} else if part.Text != "" {
			slog.Debug("response part text", "index", i, "chars", len(part.Text))
		}
	}
	slog.Debug("response metadata", "finish_reason", llmResp.FinishReason, "turn_complete", llmResp.TurnComplete)

	slog.Debug("calling yield with response")
	result := yield(llmResp, nil)
	slog.Debug("yield returned", "result", result)
}

// generateStreaming handles streaming response.
func (m *AnthropicModel) generateStreaming(ctx context.Context, req anthropic.MessageNewParams, yield func(*adkmodel.LLMResponse, error) bool) {
	stream := m.client.Messages.NewStreaming(ctx, req)

	var accumulatedText string
	var finishReason genai.FinishReason
	var toolCalls []*genai.Part

	// Track current tool use block being built
	var currentToolID string
	var currentToolName string
	var currentToolInput string

	for stream.Next() {
		event := stream.Current()

		slog.Debug("stream event", "type", event.Type)

		switch event.Type {
		case "content_block_start":
			// Handle tool use blocks starting
			block := event.AsContentBlockStart()
			slog.Debug("content block start", "type", block.ContentBlock.Type)
			if block.ContentBlock.Type == "tool_use" {
				currentToolID = block.ContentBlock.ID
				currentToolName = block.ContentBlock.Name
				currentToolInput = ""
				slog.Debug("tool use starting", "id", currentToolID, "name", currentToolName)
			}

		case "content_block_delta":
			delta := event.AsContentBlockDelta()
			if delta.Delta.Type == "text_delta" {
				accumulatedText += delta.Delta.Text
				resp := &adkmodel.LLMResponse{
					Content: &genai.Content{
						Role:  "model",
						Parts: []*genai.Part{{Text: delta.Delta.Text}},
					},
					Partial: true,
				}
				if !yield(resp, nil) {
					return
				}
			} else if delta.Delta.Type == "input_json_delta" {
				// Accumulate tool input JSON
				currentToolInput += delta.Delta.PartialJSON
			}

		case "content_block_stop":
			// If we were building a tool call, finalize it
			if currentToolName != "" {
				slog.Debug("tool use complete", "id", currentToolID, "name", currentToolName, "input", currentToolInput)
				argsMap := make(map[string]any)
				if currentToolInput != "" {
					if err := json.Unmarshal([]byte(currentToolInput), &argsMap); err != nil {
						slog.Warn("failed to parse tool input JSON", "err", err)
					}
				}
				toolCalls = append(toolCalls, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   currentToolID,
						Name: currentToolName,
						Args: argsMap,
					},
				})
				// Reset for next tool
				currentToolID = ""
				currentToolName = ""
				currentToolInput = ""
			}

		case "message_delta":
			// Contains stop reason
			msgDelta := event.AsMessageDelta()
			slog.Debug("message delta", "stop_reason", msgDelta.Delta.StopReason)
			switch msgDelta.Delta.StopReason {
			case "end_turn":
				finishReason = genai.FinishReasonStop
			case "tool_use":
				finishReason = genai.FinishReasonStop
			case "max_tokens":
				finishReason = genai.FinishReasonMaxTokens
			}

		case "message_stop":
			// Build final response
			var parts []*genai.Part
			if accumulatedText != "" {
				parts = append(parts, &genai.Part{Text: accumulatedText})
			}
			parts = append(parts, toolCalls...)

			slog.Debug("message stop", "text_chars", len(accumulatedText), "tool_calls", len(toolCalls))

			resp := &adkmodel.LLMResponse{
				Content: &genai.Content{
					Role:  "model",
					Parts: parts,
				},
				FinishReason: finishReason,
				TurnComplete: true,
			}
			yield(resp, nil)
			return
		}
	}

	if err := stream.Err(); err != nil {
		slog.Error("stream error", "err", err)
		yield(nil, fmt.Errorf("stream error: %w", err))
		return
	}

	// If we get here without message_stop, still yield a final response
	var parts []*genai.Part
	if accumulatedText != "" {
		parts = append(parts, &genai.Part{Text: accumulatedText})
	}
	parts = append(parts, toolCalls...)

	resp := &adkmodel.LLMResponse{
		Content: &genai.Content{
			Role:  "model",
			Parts: parts,
		},
		FinishReason: finishReason,
		TurnComplete: true,
	}
	yield(resp, nil)
}

// convertResponse converts an Anthropic response to ADK LLMResponse.
func (m *AnthropicModel) convertResponse(resp *anthropic.Message) *adkmodel.LLMResponse {
	var parts []*genai.Part

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			parts = append(parts, &genai.Part{Text: block.Text})

		case "tool_use":
			argsMap := make(map[string]any)
			if block.Input != nil {
				// Input is json.RawMessage, unmarshal it
				json.Unmarshal(block.Input, &argsMap)
			}
			parts = append(parts, &genai.Part{
				FunctionCall: &genai.FunctionCall{
					ID:   block.ID,
					Name: block.Name,
					Args: argsMap,
				},
			})
		}
	}

	// Map stop reason to finish reason
	var finishReason genai.FinishReason
	var turnComplete bool
	switch resp.StopReason {
	case "end_turn":
		finishReason = genai.FinishReasonStop
		turnComplete = true
	case "tool_use":
		// For tool_use, the turn is NOT complete - the tool needs to be executed
		finishReason = genai.FinishReasonStop
		turnComplete = false
	case "max_tokens":
		finishReason = genai.FinishReasonMaxTokens
		turnComplete = true
	}

	slog.Debug("converted response", "stop_reason", resp.StopReason, "turn_complete", turnComplete)

	return &adkmodel.LLMResponse{
		Content: &genai.Content{
			Role:  "model",
			Parts: parts,
		},
		FinishReason: finishReason,
		TurnComplete: turnComplete,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(resp.Usage.InputTokens),
			CandidatesTokenCount: int32(resp.Usage.OutputTokens),
			TotalTokenCount:      int32(resp.Usage.InputTokens + resp.Usage.OutputTokens),
		},
	}
}
