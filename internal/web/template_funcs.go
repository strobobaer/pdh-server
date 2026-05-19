package web

import (
	"fmt"
	"html/template"
)

func TemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"dict": templateDict,
		"list": templateList,
	}
}

func templateDict(values ...interface{}) (map[string]interface{}, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict erwartet key/value-paare")
	}

	result := make(map[string]interface{}, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key %d ist kein string", i)
		}
		result[key] = values[i+1]
	}
	return result, nil
}

func templateList(values ...interface{}) []interface{} {
	return values
}
