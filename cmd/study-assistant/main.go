package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"shuai/gateway/internal/protocol"
	"time"

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

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			return runAnalyze(ctx, http.DefaultClient, gatewayURL, filePath, model, cmd.OutOrStdout())
		},
	}

	flags := analyzeCmd.Flags()
	flags.String("model", "model-a", "which model to use")
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

func runAnalyze(ctx context.Context, client *http.Client, gatewayURL, filePath, model string, out io.Writer) error {
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
		Stream: false,
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

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp protocol.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			return err
		}
		return fmt.Errorf("err msg:%s, type:%s", errResp.Error.Message, errResp.Error.Type)
	} else {
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
	}
	return nil
}
