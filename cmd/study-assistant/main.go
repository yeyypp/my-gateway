package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"shuai/gateway/internal/protocol"

	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "study-assistant analyze <file> [--model] model",
		Short: "study assistant",
	}

	analyzeCmd := &cobra.Command{
		Use:  "analyze <file>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			gatewayURL, err := cmd.Flags().GetString("gateway-url")
			if err != nil {
				return err
			}
			if _, err := url.ParseRequestURI(gatewayURL); err != nil {
				return err
			}
			model, err := cmd.Flags().GetString("model")
			if err != nil {
				return err
			}
			isStream, err := cmd.Flags().GetBool("stream")
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			if !isStream {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
			}

			return runAnalyze(ctx, http.DefaultClient, gatewayURL, filePath, model, cmd.OutOrStdout(), isStream)
		},
	}

	flags := analyzeCmd.Flags()
	flags.String("model", "model-a", "which model to use")
	flags.Bool("stream", false, "resp is stream or not")
	flags.String("gateway-url", "http://127.0.0.1:8080", "gateway url")

	rootCmd.AddCommand(analyzeCmd)
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func readInputFile(path string) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("file path can't be empty")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return content, nil
}

func runAnalyze(ctx context.Context, client *http.Client, gatewayURL, filePath, model string, out io.Writer, isStream bool) error {
	content, err := readInputFile(filePath)
	if err != nil {
		return err
	}

	chatReq := protocol.ChatCompletionRequest{
		Model: model,
		Messages: []protocol.Message{
			{
				Role:    "user",
				Content: string(content),
			},
		},
		Stream: isStream,
	}

	reqJson, err := json.Marshal(chatReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", gatewayURL+"/v1/chat/completions", bytes.NewReader(reqJson))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	requestStarted := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	gatewayReqID := resp.Header.Get(protocol.HeaderGatewayRequestID)
	if len(gatewayReqID) != 0 {
		_, err := fmt.Fprintf(out, "X-Gateway-Request-ID:%s\n", gatewayReqID)
		if err != nil {
			return fmt.Errorf("gateway write to out failed. err:%v", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if err != nil {
			return fmt.Errorf("gateway request failed: http_status=%s, read error response: %w", resp.Status, err)
		}

		var errResp protocol.ErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && hasAPIError(errResp.Error) {
			return fmt.Errorf(
				"gateway request failed: http_status=%s, type=%s, code=%s, param=%q, message=%q",
				resp.Status,
				errResp.Error.Type,
				errResp.Error.Code,
				errResp.Error.Param,
				errResp.Error.Message,
			)
		}

		responseText := strings.TrimSpace(string(body))
		if responseText == "" {
			responseText = "<empty body>"
		}
		return fmt.Errorf("gateway request failed: http_status=%s, response=%q", resp.Status, responseText)
	} else {
		if !isStream {
			var chatResp protocol.ChatCompletionResponse
			if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
				return err
			}
			if len(chatResp.Choices) == 0 {
				return errors.New("")
			}
			if _, err := out.Write([]byte(chatResp.Choices[0].Message.Content)); err != nil {
				return err
			}
		} else {
			contentType := resp.Header.Get("Content-Type")
			mediaType, _, err := mime.ParseMediaType(contentType)
			if err != nil {
				return fmt.Errorf("parse gateway stream content type %q: %w", contentType, err)
			}
			if mediaType != "text/event-stream" {
				return fmt.Errorf("gateway stream response has content type %q, want %q", mediaType, "text/event-stream")
			}
			if _, err := fmt.Fprintf(out, "Model:%s\n", model); err != nil {
				return fmt.Errorf("write stream model: %w", err)
			}
			if err := parseStream(resp.Body, out, requestStarted); err != nil {
				return err
			}
		}

	}
	return nil
}

func hasAPIError(apiErr protocol.APIError) bool {
	return apiErr.Message != "" || apiErr.Type != "" || apiErr.Code != "" || apiErr.Param != ""
}

func parseStream(body io.Reader, out io.Writer, ttftTime time.Time) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	done := false
	isTTFT := true
	curDataLine := make([]string, 0)

	processDataLine := func() error {
		if len(curDataLine) == 0 {
			return nil
		}

		// SSE 标准规定，同一个事件里的多个 data: 应当用换行连接。
		data := strings.Join(curDataLine, "\n")
		curDataLine = curDataLine[:0]

		data = strings.TrimSpace(data)
		if len(data) == 0 {
			return nil
		}
		if data == "[DONE]" {
			done = true
			return nil
		}

		var chatResp protocol.ChatCompletionStreamResponse
		if err := json.Unmarshal([]byte(data), &chatResp); err != nil {
			return fmt.Errorf("decode stream chunk: %w", err)
		}

		for _, choice := range chatResp.Choices {
			if len(choice.Delta.Content) == 0 {
				continue
			}
			if isTTFT {
				_, err := fmt.Fprintf(out, "TTFT:%dms\n", time.Since(ttftTime).Milliseconds())
				if err != nil {
					return fmt.Errorf("write stream TTFT: %w", err)
				}
				isTTFT = false
			}
			if _, err := io.WriteString(out, choice.Delta.Content); err != nil {
				return fmt.Errorf("write stream content: %w", err)
			}
		}
		return nil
	}

	for scanner.Scan() {
		// 标准 SSE 解析只移除冒号后最多一个空格，不要对正行使用TrimSpace
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := processDataLine(); err != nil {
				return err
			}
			if done {
				break
			}
			continue
		}

		before, after, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(before) != "data" {
			continue
		}

		// DONE也是一个完整SSE事件，应该在process中处理
		// if strings.TrimSpace(after) == "[DONE]" {
		// 	done = true
		// 	break
		// }

		curDataLine = append(curDataLine, strings.TrimSpace(after))
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream response: %w", err)
	}

	if !done {
		return errors.New("stream ended before [DONE]")
	}

	return nil
}
