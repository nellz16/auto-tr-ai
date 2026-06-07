package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

const allImports = `import (
"bytes"
"context"
"crypto/sha256"
"database/sql"
"encoding/base64"
"encoding/json"
"errors"
"fmt"
"io"
"log"
"math"
"net/http"
"net/url"
"os"
"strconv"
"strings"
"sync"
"time"

bin "github.com/gagliardetto/binary"
"github.com/gagliardetto/solana-go"
turso "turso.tech/database/tursogo"
)`

var fileOrder = []string{
	"main.go",
	"types.go",
	"config.go",
	"store.go",
	"scanner.go",
	"gemini.go",
	"jupiter.go",
	"trading.go",
	"telegram.go",
	"server.go",
	"util.go",
	"misc.go",
}

var targetByName = map[string]string{
	"__const": "types.go",

	// main
	"main": "main.go",

	// types
	"Env":                     "types.go",
	"App":                     "types.go",
	"Config":                  "types.go",
	"Candidate":               "types.go",
	"Position":                "types.go",
	"AIResult":                "types.go",
	"TelegramUpdateResponse":  "types.go",
	"TelegramUpdate":          "types.go",
	"TelegramMessage":         "types.go",
	"TelegramCallback":        "types.go",
	"TelegramUser":            "types.go",
	"TelegramChat":            "types.go",
	"InlineKeyboardMarkup":    "types.go",
	"InlineKeyboardButton":    "types.go",
	"DexProfile":              "types.go",
	"DexTokenPairsResponse":   "types.go",
	"DexPair":                 "types.go",
	"GeminiRequest":           "types.go",
	"GeminiSystemInstruction": "types.go",
	"GeminiContent":           "types.go",
	"GeminiPart":              "types.go",
	"GeminiGenerationConfig":  "types.go",
	"GeminiResponse":          "types.go",
	"JupiterOrderResponse":    "types.go",
	"JupiterExecuteResponse":  "types.go",

	// config
	"loadEnv":      "config.go",
	"getenv":       "config.go",
	"reloadConfig": "config.go",

	// store
	"Store":            "store.go",
	"OpenStore":        "store.go",
	"Close":            "store.go",
	"Migrate":          "store.go",
	"SeedDefaults":     "store.go",
	"GetAllSettings":   "store.go",
	"SetSetting":       "store.go",
	"GetTelegramState": "store.go",
	"SetTelegramState": "store.go",
	"SaveEvent":        "store.go",
	"SavePosition":     "store.go",
	"SaveTrade":        "store.go",
	"LoadOpenPosition": "store.go",

	// scanner
	"scannerLoop":       "scanner.go",
	"scanOnce":          "scanner.go",
	"findBestCandidate": "scanner.go",
	"candidateFromMint": "scanner.go",
	"hardFilter":        "scanner.go",
	"scoreCandidate":    "scanner.go",

	// gemini
	"analyzeWithGemini": "gemini.go",
	"callGemini":        "gemini.go",

	// jupiter
	"executeJupiterSwap":     "jupiter.go",
	"signJupiterTransaction": "jupiter.go",

	// trading
	"buyCandidate":    "trading.go",
	"sellCurrent":     "trading.go",
	"positionLoop":    "trading.go",
	"monitorPosition": "trading.go",

	// telegram
	"telegramPoller":       "telegram.go",
	"pollTelegramOnce":     "telegram.go",
	"handleTelegramUpdate": "telegram.go",
	"handleCommand":        "telegram.go",
	"setOnOff":             "telegram.go",
	"handleCallback":       "telegram.go",
	"dashboardLoop":        "telegram.go",
	"sendOrEditDashboard":  "telegram.go",
	"dashboardKeyboard":    "telegram.go",
	"dashboardText":        "telegram.go",
	"configText":           "telegram.go",
	"helpText":             "telegram.go",
	"sendMessage":          "telegram.go",
	"editMessage":          "telegram.go",
	"answerCallback":       "telegram.go",
	"telegramPost":         "telegram.go",

	// server
	"handleHealthz": "server.go",
	"handleRoot":    "server.go",

	// util
	"getJSON":      "util.go",
	"setAction":    "util.go",
	"setError":     "util.go",
	"strSetting":   "util.go",
	"boolSetting":  "util.go",
	"intSetting":   "util.go",
	"floatSetting": "util.go",
	"csvSetting":   "util.go",
	"makeID":       "util.go",
	"now":          "util.go",
	"boolInt":      "util.go",
	"boolStr":      "util.go",
	"clamp":        "util.go",
	"sha":          "util.go",
	"escape":       "util.go",
}

func main() {
	const srcFile = "main.go"
	const backupFile = "main.monolith.backup.go.txt"

	var src []byte
	var err error

	if fileExists(backupFile) {
		fmt.Println("Using existing backup:", backupFile)
		src, err = os.ReadFile(backupFile)
	} else {
		fmt.Println("Creating backup:", backupFile)
		src, err = os.ReadFile(srcFile)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(backupFile, src, 0644); err != nil {
			panic(err)
		}
	}

	if err != nil {
		panic(err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcFile, src, parser.ParseComments)
	if err != nil {
		panic(err)
	}

	out := map[string][]string{}
	unknown := []string{}

	for _, decl := range f.Decls {
		name := declName(decl)
		if name == "" {
			continue
		}

		target := targetByName[name]
		if target == "" {
			target = "misc.go"
			unknown = append(unknown, name)
		}

		start := fset.Position(decl.Pos()).Offset
		end := fset.Position(decl.End()).Offset
		chunk := strings.TrimSpace(string(src[start:end]))
		out[target] = append(out[target], chunk)
	}

	for _, name := range fileOrder {
		decls := out[name]
		if len(decls) == 0 {
			_ = os.Remove(name)
			continue
		}

		content := "package main\n\n" + allImports + "\n\n" + strings.Join(decls, "\n\n") + "\n"
		if err := os.WriteFile(name, []byte(content), 0644); err != nil {
			panic(err)
		}
		fmt.Println("wrote", name)
	}

	if len(unknown) > 0 {
		sort.Strings(unknown)
		fmt.Println("\nWARNING: declarations placed in misc.go:")
		for _, n := range unknown {
			fmt.Println("-", n)
		}
	}

	fmt.Println("\nDone. Next run: goimports + go test")
}

func declName(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		if d.Tok == token.CONST {
			return "__const"
		}
		if d.Tok == token.TYPE && len(d.Specs) > 0 {
			if ts, ok := d.Specs[0].(*ast.TypeSpec); ok {
				return ts.Name.Name
			}
		}
		if d.Tok == token.VAR {
			return "__var"
		}
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
