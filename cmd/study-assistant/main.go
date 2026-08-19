package main

import (
	"errors"
	"log"
	"os"

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
		Run: func(cmd *cobra.Command, args []string) {
			filePath := args[0]
			content, err := readInputFile(filePath)
			if err != nil {
				log.Fatal(err)
			}

			model, err := cmd.Flags().GetString("model")
			if err != nil {
				log.Fatal(err)
			}

			log.Printf("file:%s\n", filePath)
			log.Printf("bytes:%d\n", len(content))
			log.Printf("model:%s\n", model)
		},
	}
	analyzeCmd.Flags().String("model", "model-a", "which model to use")

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
