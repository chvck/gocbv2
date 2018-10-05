package gocb

import (
	"fmt"
	"time"
)

type clientStateBlock struct {
	BucketName        string
	UseMutationTokens bool
}

func (sb *clientStateBlock) Hash() string {
	return fmt.Sprintf("%s-%b",
		sb.BucketName,
		sb.UseMutationTokens)
}

type stateBlock struct {
	cluster      *Cluster
	cachedClient *client

	clientStateBlock

	ScopeName      string
	CollectionName string

	KvTimeout   time.Duration
	PersistTo   uint
	ReplicateTo uint
}

func (sb *stateBlock) getClient() *client {
	if sb.cachedClient == nil {
		panic("attempted to fetch client from incomplete state block")
	}

	return sb.cachedClient
}

func (sb *stateBlock) recacheClient() {
	if sb.cachedClient != nil && sb.cachedClient.Hash() == sb.Hash() {
		return
	}

	sb.cachedClient = sb.cluster.getClient(&sb.clientStateBlock)
}