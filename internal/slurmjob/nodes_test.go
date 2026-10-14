package slurmjob

import (
	"reflect"
	"testing"
)

const sinfoOut = "chrom1|core|mix|64|(null)\nchrom1|ood_test|mix|64|(null)\n" +
	"cqls-gpu3|cqls_gpu*|idle|48|gpu:a100:2\nneo|core|drain|64|(null)\nuncia|core|idle*|64|(null)\nloki|core|idle~|128|(null)\n"

func TestParseNodes(t *testing.T) {
	want := []Node{
		{Name: "chrom1", Partitions: []string{"core", "ood_test"}, State: "mix", CPUs: 64},
		{Name: "cqls-gpu3", Partitions: []string{"cqls_gpu"}, State: "idle", CPUs: 48, GRES: "gpu:a100:2"},
		{Name: "loki", Partitions: []string{"core"}, State: "idle", CPUs: 128},
	}
	if got := ParseNodes(sinfoOut); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestPartitions(t *testing.T) {
	want := []Partition{{"core", 2, 1}, {"ood_test", 1, 0}, {"cqls_gpu", 1, 1}}
	if got := Partitions(ParseNodes(sinfoOut)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}
