package parser

type CollectionSpec struct {
	Name string
	Type string
	Args []string
}

type Plan struct {
	Collections []CollectionSpec
}
