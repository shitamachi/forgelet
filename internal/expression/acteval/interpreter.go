// Derived from nektos/act v0.2.89 (MIT); see NOTICE and LICENSE.
package acteval

import (
	"encoding"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/rhysd/actionlint"
)

type EvaluationEnvironment struct {
	Github    map[string]interface{}
	Env       map[string]string
	Job       map[string]interface{}
	Jobs      map[string]interface{}
	Steps     map[string]interface{}
	Runner    map[string]interface{}
	Secrets   map[string]string
	Vars      map[string]string
	Strategy  map[string]interface{}
	Matrix    map[string]interface{}
	Needs     map[string]Needs
	Inputs    map[string]interface{}
	HashFiles func([]reflect.Value) (interface{}, error)
}

type Needs struct {
	Outputs map[string]string `json:"outputs"`
	Result  string            `json:"result"`
}

type Interpreter struct{ env *EvaluationEnvironment }

func NewInterpreter(env *EvaluationEnvironment) *Interpreter { return &Interpreter{env: env} }
func (impl *Interpreter) Evaluate(source string) (interface{}, error) {
	node, err := actionlint.NewExprParser().Parse(actionlint.NewExprLexer(source + "}}"))
	if err != nil {
		return nil, fmt.Errorf("parse expression: %s", err.Message)
	}
	return impl.evaluateNode(node)
}

func (impl *Interpreter) evaluateNode(exprNode actionlint.ExprNode) (interface{}, error) {
	switch node := exprNode.(type) {
	case *actionlint.VariableNode:
		return impl.evaluateVariable(node)
	case *actionlint.BoolNode:
		return node.Value, nil
	case *actionlint.NullNode:
		return nil, nil
	case *actionlint.IntNode:
		return node.Value, nil
	case *actionlint.FloatNode:
		return node.Value, nil
	case *actionlint.StringNode:
		return node.Value, nil
	case *actionlint.IndexAccessNode:
		return impl.evaluateIndexAccess(node)
	case *actionlint.ObjectDerefNode:
		return impl.evaluateObjectDeref(node)
	case *actionlint.ArrayDerefNode:
		return impl.evaluateArrayDeref(node)
	case *actionlint.NotOpNode:
		return impl.evaluateNot(node)
	case *actionlint.CompareOpNode:
		return impl.evaluateCompare(node)
	case *actionlint.LogicalOpNode:
		return impl.evaluateLogicalCompare(node)
	case *actionlint.FuncCallNode:
		return impl.evaluateFuncCall(node)
	default:
		return nil, fmt.Errorf("unknown expression node type: %s node: %+v", reflect.TypeOf(exprNode), exprNode)
	}
}

func (impl *Interpreter) evaluateVariable(variableNode *actionlint.VariableNode) (interface{}, error) {
	switch strings.ToLower(variableNode.Name) {
	case "github":
		return impl.env.Github, nil
	case "env":
		return impl.env.Env, nil
	case "job":
		return impl.env.Job, nil
	case "jobs":
		if impl.env.Jobs == nil {
			return nil, fmt.Errorf("unavailable context: jobs")
		}
		return impl.env.Jobs, nil
	case "steps":
		return impl.env.Steps, nil
	case "runner":
		return impl.env.Runner, nil
	case "secrets":
		return impl.env.Secrets, nil
	case "vars":
		return impl.env.Vars, nil
	case "strategy":
		return impl.env.Strategy, nil
	case "matrix":
		return impl.env.Matrix, nil
	case "needs":
		return impl.env.Needs, nil
	case "inputs":
		return impl.env.Inputs, nil
	case "infinity":
		return math.Inf(1), nil
	case "nan":
		return math.NaN(), nil
	default:
		return nil, fmt.Errorf("unavailable context: %s", variableNode.Name)
	}
}

func (impl *Interpreter) evaluateIndexAccess(indexAccessNode *actionlint.IndexAccessNode) (interface{}, error) {
	left, err := impl.evaluateNode(indexAccessNode.Operand)
	if err != nil {
		return nil, err
	}

	leftValue := reflect.ValueOf(left)

	right, err := impl.evaluateNode(indexAccessNode.Index)
	if err != nil {
		return nil, err
	}

	rightValue := reflect.ValueOf(right)
	if rightValue.Kind() == reflect.Float64 {
		n := rightValue.Float()
		if n == math.Trunc(n) && n >= 0 && n < 1<<31 {
			rightValue = reflect.ValueOf(int(n))
		}
	}

	switch rightValue.Kind() {
	case reflect.String:
		return impl.getPropertyValue(leftValue, rightValue.String())

	case reflect.Int:
		switch leftValue.Kind() {
		case reflect.Slice:
			if rightValue.Int() < 0 || rightValue.Int() >= int64(leftValue.Len()) {
				return nil, nil
			}
			return leftValue.Index(int(rightValue.Int())).Interface(), nil
		default:
			return nil, nil
		}

	default:
		return nil, nil
	}
}

func (impl *Interpreter) evaluateObjectDeref(objectDerefNode *actionlint.ObjectDerefNode) (interface{}, error) {
	left, err := impl.evaluateNode(objectDerefNode.Receiver)
	if err != nil {
		return nil, err
	}

	_, receiverIsDeref := objectDerefNode.Receiver.(*actionlint.ArrayDerefNode)
	if receiverIsDeref {
		return impl.getPropertyValueDereferenced(reflect.ValueOf(left), objectDerefNode.Property)
	}
	return impl.getPropertyValue(reflect.ValueOf(left), objectDerefNode.Property)
}

func (impl *Interpreter) evaluateArrayDeref(arrayDerefNode *actionlint.ArrayDerefNode) (interface{}, error) {
	left, err := impl.evaluateNode(arrayDerefNode.Receiver)
	if err != nil {
		return nil, err
	}

	return impl.getSafeValue(reflect.ValueOf(left)), nil
}

func (impl *Interpreter) getPropertyValue(left reflect.Value, property string) (value interface{}, err error) {
	switch left.Kind() {
	case reflect.Pointer:
		return impl.getPropertyValue(left.Elem(), property)

	case reflect.Struct:
		leftType := left.Type()
		for i := 0; i < leftType.NumField(); i++ {
			jsonName := leftType.Field(i).Tag.Get("json")
			if jsonName == property {
				property = leftType.Field(i).Name
				break
			}
		}

		fieldValue := left.FieldByNameFunc(func(name string) bool {
			return strings.EqualFold(name, property)
		})

		if fieldValue.Kind() == reflect.Invalid {
			return "", nil
		}

		i := fieldValue.Interface()
		// The type stepStatus int is an integer, but should be treated as string
		if m, ok := i.(encoding.TextMarshaler); ok {
			text, err := m.MarshalText()
			if err != nil {
				return nil, err
			}
			return string(text), nil
		}
		return i, nil

	case reflect.Map:
		iter := left.MapRange()

		for iter.Next() {
			key := iter.Key()

			switch key.Kind() {
			case reflect.String:
				if strings.EqualFold(key.String(), property) {
					return impl.getMapValue(iter.Value())
				}

			default:
				return nil, fmt.Errorf("'%s' in map key not implemented", key.Kind())
			}
		}

		return nil, nil

	case reflect.Slice:
		var values []interface{}

		for i := 0; i < left.Len(); i++ {
			value, err := impl.getPropertyValue(left.Index(i).Elem(), property)
			if err != nil {
				return nil, err
			}

			values = append(values, value)
		}

		return values, nil
	}

	return nil, nil
}

func (impl *Interpreter) getPropertyValueDereferenced(left reflect.Value, property string) (value interface{}, err error) {
	switch left.Kind() {
	case reflect.Pointer:
		return impl.getPropertyValue(left, property)

	case reflect.Struct:
		return impl.getPropertyValue(left, property)
	case reflect.Map:
		iter := left.MapRange()

		var values []interface{}
		for iter.Next() {
			value, err := impl.getPropertyValue(iter.Value(), property)
			if err != nil {
				return nil, err
			}

			values = append(values, value)
		}

		return values, nil
	case reflect.Slice:
		return impl.getPropertyValue(left, property)
	}

	return nil, nil
}

func (impl *Interpreter) getMapValue(value reflect.Value) (interface{}, error) {
	if !value.IsValid() {
		return nil, nil
	}
	if value.Kind() == reflect.Pointer {
		return impl.getMapValue(value.Elem())
	}

	return value.Interface(), nil
}

func (impl *Interpreter) evaluateNot(notNode *actionlint.NotOpNode) (interface{}, error) {
	operand, err := impl.evaluateNode(notNode.Operand)
	if err != nil {
		return nil, err
	}

	return !IsTruthy(operand), nil
}

func (impl *Interpreter) evaluateCompare(compareNode *actionlint.CompareOpNode) (interface{}, error) {
	left, err := impl.evaluateNode(compareNode.Left)
	if err != nil {
		return nil, err
	}

	right, err := impl.evaluateNode(compareNode.Right)
	if err != nil {
		return nil, err
	}

	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if rightValue.Kind() == reflect.Float64 {
		n := rightValue.Float()
		if n == math.Trunc(n) && n >= 0 && n < 1<<31 {
			rightValue = reflect.ValueOf(int(n))
		}
	}

	return impl.compareValues(leftValue, rightValue, compareNode.Kind)
}

func (impl *Interpreter) compareValues(leftValue reflect.Value, rightValue reflect.Value, kind actionlint.CompareOpNodeKind) (interface{}, error) {
	if leftValue.Kind() != rightValue.Kind() {
		if !impl.isNumber(leftValue) {
			leftValue = impl.coerceToNumber(leftValue)
		}
		if !impl.isNumber(rightValue) {
			rightValue = impl.coerceToNumber(rightValue)
		}
	}

	switch leftValue.Kind() {
	case reflect.Bool:
		return impl.compareNumber(float64(impl.coerceToNumber(leftValue).Int()), float64(impl.coerceToNumber(rightValue).Int()), kind)
	case reflect.String:
		return impl.compareString(strings.ToLower(leftValue.String()), strings.ToLower(rightValue.String()), kind)

	case reflect.Int:
		if rightValue.Kind() == reflect.Float64 {
			return impl.compareNumber(float64(leftValue.Int()), rightValue.Float(), kind)
		}

		return impl.compareNumber(float64(leftValue.Int()), float64(rightValue.Int()), kind)

	case reflect.Float64:
		if rightValue.Kind() == reflect.Int {
			return impl.compareNumber(leftValue.Float(), float64(rightValue.Int()), kind)
		}

		return impl.compareNumber(leftValue.Float(), rightValue.Float(), kind)

	case reflect.Invalid:
		return impl.compareNumber(0, 0, kind)

	default:
		return nil, fmt.Errorf("comparison not implemented for types: left: %+v, right: %+v", leftValue.Kind(), rightValue.Kind())
	}
}

func (impl *Interpreter) coerceToNumber(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Invalid:
		return reflect.ValueOf(0)

	case reflect.Bool:
		switch value.Bool() {
		case true:
			return reflect.ValueOf(1)
		case false:
			return reflect.ValueOf(0)
		}

	case reflect.String:
		if value.String() == "" {
			return reflect.ValueOf(0)
		}

		// Numeric coercion must not evaluate an expression from a data value.
		return reflect.ValueOf(parseNumber(value.String()))
	}

	return reflect.ValueOf(math.NaN())
}

func (impl *Interpreter) coerceToString(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Invalid:
		return reflect.ValueOf("")

	case reflect.Bool:
		switch value.Bool() {
		case true:
			return reflect.ValueOf("true")
		case false:
			return reflect.ValueOf("false")
		}

	case reflect.String:
		return value

	case reflect.Int:
		return reflect.ValueOf(fmt.Sprint(value))

	case reflect.Float64:
		if math.IsInf(value.Float(), 1) {
			return reflect.ValueOf("Infinity")
		} else if math.IsInf(value.Float(), -1) {
			return reflect.ValueOf("-Infinity")
		}
		return reflect.ValueOf(fmt.Sprintf("%.15G", value.Float()))

	case reflect.Slice:
		return reflect.ValueOf("Array")

	case reflect.Map:
		return reflect.ValueOf("Object")
	}

	return value
}

func (impl *Interpreter) compareString(left string, right string, kind actionlint.CompareOpNodeKind) (bool, error) {
	switch kind {
	case actionlint.CompareOpNodeKindLess:
		return left < right, nil
	case actionlint.CompareOpNodeKindLessEq:
		return left <= right, nil
	case actionlint.CompareOpNodeKindGreater:
		return left > right, nil
	case actionlint.CompareOpNodeKindGreaterEq:
		return left >= right, nil
	case actionlint.CompareOpNodeKindEq:
		return left == right, nil
	case actionlint.CompareOpNodeKindNotEq:
		return left != right, nil
	default:
		return false, fmt.Errorf("TODO: not implemented to compare '%+v'", kind)
	}
}

func (impl *Interpreter) compareNumber(left float64, right float64, kind actionlint.CompareOpNodeKind) (bool, error) {
	switch kind {
	case actionlint.CompareOpNodeKindLess:
		return left < right, nil
	case actionlint.CompareOpNodeKindLessEq:
		return left <= right, nil
	case actionlint.CompareOpNodeKindGreater:
		return left > right, nil
	case actionlint.CompareOpNodeKindGreaterEq:
		return left >= right, nil
	case actionlint.CompareOpNodeKindEq:
		return left == right, nil
	case actionlint.CompareOpNodeKindNotEq:
		return left != right, nil
	default:
		return false, fmt.Errorf("TODO: not implemented to compare '%+v'", kind)
	}
}

func IsTruthy(input interface{}) bool {
	value := reflect.ValueOf(input)
	switch value.Kind() {
	case reflect.Bool:
		return value.Bool()

	case reflect.String:
		return value.String() != ""

	case reflect.Int:
		return value.Int() != 0

	case reflect.Float64:
		if math.IsNaN(value.Float()) {
			return false
		}

		return value.Float() != 0

	case reflect.Map, reflect.Slice:
		return true

	default:
		return false
	}
}

func (impl *Interpreter) isNumber(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Int, reflect.Float64:
		return true
	default:
		return false
	}
}

func (impl *Interpreter) getSafeValue(value reflect.Value) interface{} {
	switch value.Kind() {
	case reflect.Invalid:
		return nil

	case reflect.Float64:
		if value.Float() == 0 {
			return 0
		}
	}

	return value.Interface()
}

func (impl *Interpreter) evaluateLogicalCompare(compareNode *actionlint.LogicalOpNode) (interface{}, error) {
	left, err := impl.evaluateNode(compareNode.Left)
	if err != nil {
		return nil, err
	}

	leftValue := reflect.ValueOf(left)

	if IsTruthy(left) == (compareNode.Kind == actionlint.LogicalOpNodeKindOr) {
		return impl.getSafeValue(leftValue), nil
	}

	right, err := impl.evaluateNode(compareNode.Right)
	if err != nil {
		return nil, err
	}

	rightValue := reflect.ValueOf(right)
	if rightValue.Kind() == reflect.Float64 {
		n := rightValue.Float()
		if n == math.Trunc(n) && n >= 0 && n < 1<<31 {
			rightValue = reflect.ValueOf(int(n))
		}
	}

	switch compareNode.Kind {
	case actionlint.LogicalOpNodeKindAnd:
		return impl.getSafeValue(rightValue), nil
	case actionlint.LogicalOpNodeKindOr:
		return impl.getSafeValue(rightValue), nil
	}

	return nil, fmt.Errorf("unable to compare incompatible types '%s' and '%s'", leftValue.Kind(), rightValue.Kind())
}

//nolint:gocyclo
func (impl *Interpreter) evaluateFuncCall(funcCallNode *actionlint.FuncCallNode) (interface{}, error) {
	args := make([]reflect.Value, 0)

	for _, arg := range funcCallNode.Args {
		value, err := impl.evaluateNode(arg)
		if err != nil {
			return nil, err
		}

		args = append(args, reflect.ValueOf(value))
	}

	switch strings.ToLower(funcCallNode.Callee) {
	case "contains":
		return impl.contains(args[0], args[1])
	case "startswith":
		return impl.startsWith(args[0], args[1])
	case "endswith":
		return impl.endsWith(args[0], args[1])
	case "format":
		return impl.format(args[0], args[1:]...)
	case "join":
		if len(args) == 1 {
			return impl.join(args[0], reflect.ValueOf(","))
		}
		return impl.join(args[0], args[1])
	case "tojson":
		return impl.toJSON(args[0])
	case "fromjson":
		return impl.fromJSON(args[0])
	case "hashfiles":
		if impl.env.HashFiles != nil {
			return impl.env.HashFiles(args)
		}
		return nil, fmt.Errorf("hashFiles requires an explicit workspace callback")
	case "always", "success", "failure", "cancelled":
		return nil, fmt.Errorf("status functions require the forgelet adapter")
	default:
		return nil, fmt.Errorf("TODO: '%s' not implemented", funcCallNode.Callee)
	}
}
