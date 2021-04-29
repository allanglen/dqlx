package dqlx

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type Operation interface {
	DQLizer
	GetName() string
}

type queryOperation struct {
	operations []Operation
	variables  []Operation
}

func QueriesToDQL(queries ...QueryBuilder) (query string, args map[string]string, err error) {
	mainOperation := queryOperation{}
	queries = ensureUniqueQueryNames(queries)

	for _, query := range queries {
		mainOperation.operations = append(mainOperation.operations, query.rootEdge)

		for _, variable := range query.variables {
			mainOperation.variables = append(mainOperation.variables, variable.rootEdge)
		}
	}

	return mainOperation.ToDQL()
}

func (grammar queryOperation) ToDQL() (query string, variables map[string]string, err error) {
	variables = map[string]string{}
	blocNames := make([]string, len(grammar.operations))

	for index, block := range grammar.operations {
		blocNames[index] = strings.Title(strings.ToLower(block.GetName()))
	}

	queryName := strings.Join(blocNames, "_")

	var args []interface{}
	var statements []string

	if err := addOperation(grammar.variables, &statements, &args); err != nil {
		return "", nil, err
	}

	if err := addOperation(grammar.operations, &statements, &args); err != nil {
		return "", nil, err
	}

	innerQuery := strings.Join(statements, " ")

	query, rawVariables := replacePlaceholders(innerQuery, args, func(index int, value interface{}) string {
		return fmt.Sprintf("$%d", index)
	})
	variables, placeholders := toVariables(rawVariables)

	writer := bytes.Buffer{}
	writer.WriteString(fmt.Sprintf("query %s(%s) {", queryName, strings.Join(placeholders, ", ")))
	writer.WriteString(" " + query)
	writer.WriteString(" }")

	return writer.String(), variables, nil
}

func toVariables(rawVariables map[int]interface{}) (variables map[string]string, placeholders []string) {
	variables = map[string]string{}
	placeholders = make([]string, len(rawVariables))

	queryPlaceholderNames := getSortedVariables(rawVariables)

	// Format Variables
	for index, placeholderName := range queryPlaceholderNames {
		variableName := fmt.Sprintf("$%d", placeholderName)

		variables[variableName] = toVariableValue(rawVariables[index])
		placeholders[index] = fmt.Sprintf("$%d:%s", placeholderName, goTypeToDQLType(rawVariables[placeholderName]))
	}

	return variables, placeholders
}

func toVariableValue(value interface{}) string {
	switch val := value.(type) {
	case time.Time:
		return val.Format(time.RFC3339)
	case *time.Time:
		return val.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func ensureUniqueQueryNames(queries []QueryBuilder) []QueryBuilder {
	queryNames := map[string]bool{}
	uniqueQueries := make([]QueryBuilder, len(queries))

	for index, query := range queries {
		if queryNames[query.rootEdge.Name] {
			query = query.Name(fmt.Sprintf("%s_%d", query.rootEdge.Name, index))
		}

		queryNames[query.rootEdge.Name] = true
		uniqueQueries[index] = query
	}

	return uniqueQueries
}

func goTypeToDQLType(value interface{}) string {
	switch value.(type) {
	case string:
		return "string"
	case int, int8, int32, int64:
		return "int"
	case float32, float64:
		return "float"
	case bool:
		return "bool"
	case time.Time, *time.Time:
		return "datetime"
	}

	return "string"
}

func replacePlaceholders(query string, args []interface{}, transform func(index int, value interface{}) string) (string, map[int]interface{}) {
	variables := map[int]interface{}{}
	buf := &bytes.Buffer{}
	i := 0

	for {
		p := strings.Index(query, "??")
		if p == -1 {
			break
		}

		buf.WriteString(query[:p])
		key := transform(i, args[i])
		buf.WriteString(key)
		query = query[p+2:]

		// Assign the variables
		variables[i] = args[i]

		i++
	}

	buf.WriteString(query)
	return buf.String(), variables
}

func isListType(val interface{}) bool {
	valVal := reflect.ValueOf(val)
	return valVal.Kind() == reflect.Array || valVal.Kind() == reflect.Slice
}

func addOperation(operations []Operation, statements *[]string, args *[]interface{}) error {
	parts := make([]DQLizer, len(operations))

	for index, operation := range operations {
		parts[index] = operation
	}

	return addStatement(parts, statements, args)
}

func addStatement(parts []DQLizer, statements *[]string, args *[]interface{}) error {
	for _, block := range parts {
		statement, queryArg, err := block.ToDQL()

		if err != nil {
			return err
		}

		*statements = append(*statements, statement)
		*args = append(*args, queryArg...)
	}

	return nil
}

func addPart(part DQLizer, writer *bytes.Buffer, args *[]interface{}) error {
	statement, statementArgs, err := part.ToDQL()
	*args = append(*args, statementArgs...)

	if err != nil {
		return err
	}

	writer.WriteString(statement)

	return nil
}
