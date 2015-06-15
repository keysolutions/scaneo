package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"
)

const (
	usageText = `SCANEO
    Generate Go code to convert database rows into arbitrary structs.

USAGE
    scaneo [options] paths...

OPTIONS
    -o, -output
        Set the name of the generated file. Default is scans.go.

    -p, -package
        Set the package name for the generated file. Default is current
        directory name.

    -u, -unexport
        Generate unexported functions. Default is export all.

    -w, -white-list
        Only include structs specified in case-sensitive, comma-delimited
        string.

    -v, -version
        Print version and exit.

    -h, -help
        Print help and exit.

EXAMPLES
    tables.go is a file that contains one or more struct declarations.

    Generate scan functions based on structs in tables.go.
        scaneo tables.go

    Generate scan functions and name the output file funcs.go
        scaneo -o funcs.go tables.go

    Generate scans.go with unexported functions.
        scaneo -u tables.go

    Generate scans.go with only struct Post and struct user.
        scaneo -w "Post,user" tables.go

NOTES
    Struct field names don't have to match database column names at all.
    However, the order of the types must match.

    Integrate this with go generate by adding this line to the top of your
    tables.go file.
        //go:generate scaneo $GOFILE
`
)

type structToken struct {
	Name   string
	Fields []string
	Types  []string
}

var (
	scansTmpl = template.Must(template.New("scans").Parse(scansText))

	outFilename = flag.String("o", "scans.go", "")
	packName    = flag.String("p", "current directory", "")
	unexport    = flag.Bool("u", false, "")
	whiteList   = flag.String("w", "", "")
	version     = flag.Bool("v", false, "")
	help        = flag.Bool("h", false, "")
)

func init() {
	flag.StringVar(outFilename, "output", "scans.go", "")
	flag.StringVar(packName, "package", "current directory", "")
	flag.BoolVar(unexport, "unexport", false, "")
	flag.StringVar(whiteList, "white-list", "", "")
	flag.BoolVar(version, "version", false, "")
	flag.BoolVar(help, "help", false, "")

	flag.Usage = func() {
		log.Println(usageText)
	}
}

func main() {
	log.SetFlags(0)
	flag.Parse()

	if *help {
		fmt.Println(usageText)
		return
	}

	if *version {
		fmt.Println("scaneo version 1.1.0")
		return
	}

	inputPaths := flag.Args()
	if len(inputPaths) == 0 {
		log.Println("missing input paths")
		log.Println(usageText)
		os.Exit(1)
	}

	if *packName == "current directory" {
		wd, err := os.Getwd()
		if err != nil {
			log.Fatalln("couldn't get working directory:", err)
		}

		*packName = filepath.Base(wd)
	}

	files, err := filenames(inputPaths)
	if err != nil {
		log.Fatalln("couldn't get filenames:", err)
	}

	wSplits := strings.Split(*whiteList, ",")
	whiteList := make(map[string]struct{}, len(wSplits))
	for _, s := range wSplits {
		whiteList[s] = struct{}{}
	}

	structToks := make([]structToken, 0, 8)
	for _, file := range files {
		toks, err := parseCode(file, whiteList)
		if err != nil {
			log.Println(`"syntax error" - parser probably`)
			log.Fatal(err)
		}

		structToks = append(structToks, toks...)
	}

	fout, err := os.Create(*outFilename)
	if err != nil {
		log.Fatal(err)
	}
	defer fout.Close()

	if err := genFile(fout, *packName, *unexport, structToks); err != nil {
		log.Fatalln("couldn't generate file:", err)
	}
}

func filenames(paths []string) ([]string, error) {
	files := make([]string, 0, 8)

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}

		if info.IsDir() {
			filepath.Walk(path, func(startDir string, subInfo os.FileInfo, _ error) error {
				if subInfo.IsDir() {
					return nil
				} else if subInfo.Name()[0] == '.' {
					return nil
				}

				files = append(files, startDir)
				return nil
			})

			continue
		}

		files = append(files, path)
	}

	fileMap := make(map[string]struct{})
	for _, f := range files {
		fileMap[f] = struct{}{}
	}

	deduped := make([]string, 0, len(fileMap))
	for f := range fileMap {
		deduped = append(deduped, f)
	}

	return deduped, nil
}

func parseCode(srcFile string, iSplits map[string]struct{}) ([]structToken, error) {
	structToks := make([]structToken, 0, 8)

	fset := token.NewFileSet()
	astf, err := parser.ParseFile(fset, srcFile, nil, 0)
	if err != nil {
		return nil, err
	}

	var filter bool
	if len(iSplits) > 0 {
		filter = true
	}

	// ast.Print(fset, astf)
	for _, decl := range astf.Decls {
		structTok := structToken{
			Fields: make([]string, 0, 8),
			Types:  make([]string, 0, 8),
		}

		genDecl, isGenDecl := decl.(*ast.GenDecl)
		if !isGenDecl {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, isTypeSpec := spec.(*ast.TypeSpec)
			if !isTypeSpec {
				continue
			}

			// filter, if necessary
			if !filter {
				structTok.Name = typeSpec.Name.Name
			} else if _, exists := iSplits[typeSpec.Name.Name]; filter && !exists {
				continue
			} else if filter && exists {
				structTok.Name = typeSpec.Name.Name
			}

			structType, isStructType := typeSpec.Type.(*ast.StructType)
			if !isStructType {
				continue
			}

			for _, field := range structType.Fields.List {
				for _, ident := range field.Names {
					structTok.Fields = append(structTok.Fields, ident.Name)
				}

				switch fieldType := field.Type.(type) {
				case *ast.Ident:
					structTok.Types = append(structTok.Types, fieldType.Name)
				case *ast.SelectorExpr:
					ident, isIdent := fieldType.X.(*ast.Ident)
					if !isIdent {
						continue
					}

					structTok.Types = append(structTok.Types,
						fmt.Sprint(ident.Name, ".", fieldType.Sel.Name))
				case *ast.StarExpr:
					selExp, isSelector := fieldType.X.(*ast.SelectorExpr)
					if !isSelector {
						continue
					}

					ident, isIdent := selExp.X.(*ast.Ident)
					if !isIdent {
						continue
					}

					structTok.Types = append(structTok.Types,
						fmt.Sprint("*", ident.Name, ".", selExp.Sel.Name))
				case *ast.ArrayType:
					ident, isIdent := fieldType.Elt.(*ast.Ident)
					if !isIdent {
						continue
					}

					structTok.Types = append(structTok.Types,
						fmt.Sprint("[]", ident.Name))
				}

			}

			structToks = append(structToks, structTok)
		}
	}

	return structToks, nil
}

func genFile(fout *os.File, pkg string, unexport bool, toks []structToken) error {
	data := struct {
		PackageName string
		Tokens      []structToken
		Access      string
	}{
		PackageName: pkg,
		Tokens:      make([]structToken, len(toks)),
		Access:      "S",
	}

	// make funcs scanFoo or ScanFoo, never scanfoo or Scanfoo
	for i := range toks {
		data.Tokens[i] = toks[i]
		data.Tokens[i].Name = string(unicode.ToTitle(rune(toks[i].Name[0]))) +
			toks[i].Name[1:]
	}

	if unexport {
		data.Access = "s"
	}

	if err := scansTmpl.Execute(fout, data); err != nil {
		return err
	}

	return nil
}
