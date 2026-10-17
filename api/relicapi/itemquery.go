package relicapi

import (
	"fmt"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/artifacts/itemquery"
)

const maxCriteriaDepth = 32

var itemFields = map[relicv1.ItemField]itemquery.Field{
	relicv1.ItemField_ITEM_FIELD_REPOSITORY:  itemquery.FieldRepository,
	relicv1.ItemField_ITEM_FIELD_PATH:        itemquery.FieldPath,
	relicv1.ItemField_ITEM_FIELD_NAME:        itemquery.FieldName,
	relicv1.ItemField_ITEM_FIELD_TYPE:        itemquery.FieldType,
	relicv1.ItemField_ITEM_FIELD_SIZE:        itemquery.FieldSize,
	relicv1.ItemField_ITEM_FIELD_CREATED:     itemquery.FieldCreated,
	relicv1.ItemField_ITEM_FIELD_CREATED_BY:  itemquery.FieldCreatedBy,
	relicv1.ItemField_ITEM_FIELD_MODIFIED:    itemquery.FieldModified,
	relicv1.ItemField_ITEM_FIELD_MODIFIED_BY: itemquery.FieldModifiedBy,
	relicv1.ItemField_ITEM_FIELD_UPDATED:     itemquery.FieldUpdated,
	relicv1.ItemField_ITEM_FIELD_SHA1:        itemquery.FieldSHA1,
	relicv1.ItemField_ITEM_FIELD_MD5:         itemquery.FieldMD5,
	relicv1.ItemField_ITEM_FIELD_SHA256:      itemquery.FieldSHA256,
	relicv1.ItemField_ITEM_FIELD_DEPTH:       itemquery.FieldDepth,
}

var comparisonOperators = map[relicv1.ComparisonOperator]itemquery.Op{
	relicv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL:            itemquery.OpEqual,
	relicv1.ComparisonOperator_COMPARISON_OPERATOR_NOT_EQUAL:        itemquery.OpNotEqual,
	relicv1.ComparisonOperator_COMPARISON_OPERATOR_GREATER:          itemquery.OpGreater,
	relicv1.ComparisonOperator_COMPARISON_OPERATOR_GREATER_OR_EQUAL: itemquery.OpGreaterOrEqual,
	relicv1.ComparisonOperator_COMPARISON_OPERATOR_LESS:             itemquery.OpLess,
	relicv1.ComparisonOperator_COMPARISON_OPERATOR_LESS_OR_EQUAL:    itemquery.OpLessOrEqual,
	relicv1.ComparisonOperator_COMPARISON_OPERATOR_MATCH:            itemquery.OpMatch,
	relicv1.ComparisonOperator_COMPARISON_OPERATOR_NOT_MATCH:        itemquery.OpNotMatch,
}

var buildRelations = map[relicv1.ItemBuildRelation]itemquery.BuildRelation{
	relicv1.ItemBuildRelation_ITEM_BUILD_RELATION_ARTIFACT:   itemquery.BuildArtifact,
	relicv1.ItemBuildRelation_ITEM_BUILD_RELATION_DEPENDENCY: itemquery.BuildDependency,
}

var buildRunFields = map[relicv1.BuildRunField]itemquery.BuildField{
	relicv1.BuildRunField_BUILD_RUN_FIELD_NAME:   itemquery.BuildName,
	relicv1.BuildRunField_BUILD_RUN_FIELD_NUMBER: itemquery.BuildNumber,
}

func itemQueryOf(m *relicv1.ItemQuery) (itemquery.Query, error) {
	if m.GetCriteria() == nil {
		return itemquery.Query{}, nil
	}

	n, err := criterionOf(m.GetCriteria(), 0)

	return itemquery.Query{
		Criteria: n,
	}, err
}

func criterionOf(c *relicv1.ItemCriterion, depth int) (itemquery.Node, error) {
	if depth > maxCriteriaDepth {
		return nil, fmt.Errorf("%w: criteria nest deeper than %d", itemquery.ErrInvalid, maxCriteriaDepth)
	}

	children := func(g *relicv1.ItemCriteria) ([]itemquery.Node, error) {
		out := make([]itemquery.Node, 0, len(g.GetCriteria()))
		for _, child := range g.GetCriteria() {
			n, err := criterionOf(child, depth+1)
			if err != nil {
				return nil, err
			}

			out = append(out, n)
		}

		return out, nil
	}

	switch k := c.GetCriterion().(type) {
	case *relicv1.ItemCriterion_All:
		n, err := children(k.All)
		return itemquery.All(n), err
	case *relicv1.ItemCriterion_Any:
		n, err := children(k.Any)
		return itemquery.Any(n), err
	case *relicv1.ItemCriterion_SameProperty:
		out := itemquery.SameProperty{}

		for _, child := range k.SameProperty.GetCriteria() {
			p := child.GetProperty()
			if p == nil {
				return nil, fmt.Errorf("%w: same_property holds property comparisons only", itemquery.ErrInvalid)
			}

			n, err := propertyOf(p)
			if err != nil {
				return nil, err
			}

			out = append(out, n)
		}

		return out, nil
	case *relicv1.ItemCriterion_Property:
		return propertyOf(k.Property)
	case *relicv1.ItemCriterion_Field:
		f, ok := itemFields[k.Field.GetField()]
		if !ok {
			return nil, fmt.Errorf("%w: field %v", itemquery.ErrInvalid, k.Field.GetField())
		}

		op, err := operatorOf(k.Field.GetOperator())
		if err != nil {
			return nil, err
		}

		v, err := valueOf(k.Field.GetValue())

		return itemquery.Compare{
			Field: f,
			Op:    op,
			Value: v,
		}, err
	case *relicv1.ItemCriterion_Build:
		rel, ok := buildRelations[k.Build.GetRelation()]
		if !ok {
			return nil, fmt.Errorf("%w: build relation %v", itemquery.ErrInvalid, k.Build.GetRelation())
		}

		f, ok := buildRunFields[k.Build.GetField()]
		if !ok {
			return nil, fmt.Errorf("%w: build run field %v", itemquery.ErrInvalid, k.Build.GetField())
		}

		op, err := operatorOf(k.Build.GetOperator())

		return itemquery.Build{
			Relation: rel,
			Field:    f,
			Op:       op,
			Value:    k.Build.GetValue(),
		}, err
	}

	return nil, fmt.Errorf("%w: an empty criterion", itemquery.ErrInvalid)
}

func propertyOf(p *relicv1.ItemPropertyComparison) (itemquery.Node, error) {
	op, err := operatorOf(p.GetOperator())
	if err != nil {
		return nil, err
	}

	if p.GetCompareKey() {
		return itemquery.PropertyKey{
			Op:    op,
			Value: p.GetValue(),
		}, nil
	}

	return itemquery.PropertyValue{
		Key:   p.GetKey(),
		Op:    op,
		Value: p.GetValue(),
	}, nil
}

func operatorOf(o relicv1.ComparisonOperator) (itemquery.Op, error) {
	if op, ok := comparisonOperators[o]; ok {
		return op, nil
	}

	return "", fmt.Errorf("%w: operator %v", itemquery.ErrInvalid, o)
}

func valueOf(v *relicv1.ComparisonValue) (itemquery.Value, error) {
	switch k := v.GetValue().(type) {
	case *relicv1.ComparisonValue_Text:
		return k.Text, nil
	case *relicv1.ComparisonValue_Number:
		return k.Number, nil
	case *relicv1.ComparisonValue_Time:
		return k.Time.AsTime(), nil
	case *relicv1.ComparisonValue_Missing:
		return nil, nil
	}

	return nil, fmt.Errorf("%w: a comparison without a value", itemquery.ErrInvalid)
}
