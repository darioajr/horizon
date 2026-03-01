// Package protocol implements the Kafka wire protocol for Horizon.
// It provides encoding/decoding of Kafka messages following the official specification.
package protocol

// ApiKey represents Kafka API request types
type ApiKey int16

const (
	ApiKeyProduce             ApiKey = 0
	ApiKeyFetch               ApiKey = 1
	ApiKeyListOffsets         ApiKey = 2
	ApiKeyMetadata            ApiKey = 3
	ApiKeyLeaderAndIsr        ApiKey = 4
	ApiKeyStopReplica         ApiKey = 5
	ApiKeyUpdateMetadata      ApiKey = 6
	ApiKeyControlledShutdown  ApiKey = 7
	ApiKeyOffsetCommit        ApiKey = 8
	ApiKeyOffsetFetch         ApiKey = 9
	ApiKeyFindCoordinator     ApiKey = 10
	ApiKeyJoinGroup           ApiKey = 11
	ApiKeyHeartbeat           ApiKey = 12
	ApiKeyLeaveGroup          ApiKey = 13
	ApiKeySyncGroup           ApiKey = 14
	ApiKeyDescribeGroups      ApiKey = 15
	ApiKeyListGroups          ApiKey = 16
	ApiKeySaslHandshake       ApiKey = 17
	ApiKeyApiVersions         ApiKey = 18
	ApiKeyCreateTopics        ApiKey = 19
	ApiKeyDeleteTopics        ApiKey = 20
	ApiKeyDeleteRecords       ApiKey = 21
	ApiKeyInitProducerId      ApiKey = 22
	ApiKeyOffsetForLeaderEpoch ApiKey = 23
	ApiKeyAddPartitionsToTxn  ApiKey = 24
	ApiKeyAddOffsetsToTxn     ApiKey = 25
	ApiKeyEndTxn              ApiKey = 26
	ApiKeyWriteTxnMarkers     ApiKey = 27
	ApiKeyTxnOffsetCommit     ApiKey = 28
	ApiKeyDescribeAcls        ApiKey = 29
	ApiKeyCreateAcls          ApiKey = 30
	ApiKeyDeleteAcls          ApiKey = 31
	ApiKeyDescribeConfigs     ApiKey = 32
	ApiKeyAlterConfigs        ApiKey = 33
	ApiKeyAlterReplicaLogDirs ApiKey = 34
	ApiKeyDescribeLogDirs     ApiKey = 35
	ApiKeySaslAuthenticate    ApiKey = 36
	ApiKeyCreatePartitions    ApiKey = 37
	ApiKeyDeleteGroups        ApiKey = 42
)

// String returns the name of the API key
func (k ApiKey) String() string {
	names := map[ApiKey]string{
		ApiKeyProduce:             "Produce",
		ApiKeyFetch:               "Fetch",
		ApiKeyListOffsets:         "ListOffsets",
		ApiKeyMetadata:            "Metadata",
		ApiKeyOffsetCommit:        "OffsetCommit",
		ApiKeyOffsetFetch:         "OffsetFetch",
		ApiKeyFindCoordinator:     "FindCoordinator",
		ApiKeyJoinGroup:           "JoinGroup",
		ApiKeyHeartbeat:           "Heartbeat",
		ApiKeyLeaveGroup:          "LeaveGroup",
		ApiKeySyncGroup:           "SyncGroup",
		ApiKeyDescribeGroups:      "DescribeGroups",
		ApiKeyListGroups:          "ListGroups",
		ApiKeyApiVersions:         "ApiVersions",
		ApiKeyCreateTopics:        "CreateTopics",
		ApiKeyDeleteTopics:        "DeleteTopics",
		ApiKeyDeleteRecords:       "DeleteRecords",
		ApiKeyDescribeConfigs:     "DescribeConfigs",
		ApiKeyDeleteGroups:        "DeleteGroups",
	}
	if name, ok := names[k]; ok {
		return name
	}
	return "Unknown"
}

// ErrorCode represents Kafka error codes
type ErrorCode int16

const (
	ErrNone                          ErrorCode = 0
	ErrUnknown                       ErrorCode = -1
	ErrOffsetOutOfRange              ErrorCode = 1
	ErrCorruptMessage                ErrorCode = 2
	ErrUnknownTopicOrPartition       ErrorCode = 3
	ErrInvalidFetchSize              ErrorCode = 4
	ErrLeaderNotAvailable            ErrorCode = 5
	ErrNotLeaderForPartition         ErrorCode = 6
	ErrRequestTimedOut               ErrorCode = 7
	ErrBrokerNotAvailable            ErrorCode = 8
	ErrReplicaNotAvailable           ErrorCode = 9
	ErrMessageTooLarge               ErrorCode = 10
	ErrStaleControllerEpoch          ErrorCode = 11
	ErrOffsetMetadataTooLarge        ErrorCode = 12
	ErrNetworkException              ErrorCode = 13
	ErrCoordinatorLoadInProgress     ErrorCode = 14
	ErrCoordinatorNotAvailable       ErrorCode = 15
	ErrNotCoordinator                ErrorCode = 16
	ErrInvalidTopicException         ErrorCode = 17
	ErrRecordListTooLarge            ErrorCode = 18
	ErrNotEnoughReplicas             ErrorCode = 19
	ErrNotEnoughReplicasAfterAppend  ErrorCode = 20
	ErrInvalidRequiredAcks           ErrorCode = 21
	ErrIllegalGeneration             ErrorCode = 22
	ErrInconsistentGroupProtocol     ErrorCode = 23
	ErrInvalidGroupId                ErrorCode = 24
	ErrUnknownMemberId               ErrorCode = 25
	ErrInvalidSessionTimeout         ErrorCode = 26
	ErrRebalanceInProgress           ErrorCode = 27
	ErrInvalidCommitOffsetSize       ErrorCode = 28
	ErrTopicAuthorizationFailed      ErrorCode = 29
	ErrGroupAuthorizationFailed      ErrorCode = 30
	ErrClusterAuthorizationFailed    ErrorCode = 31
	ErrInvalidTimestamp              ErrorCode = 32
	ErrUnsupportedSaslMechanism      ErrorCode = 33
	ErrIllegalSaslState              ErrorCode = 34
	ErrUnsupportedVersion            ErrorCode = 35
	ErrTopicAlreadyExists            ErrorCode = 36
	ErrInvalidPartitions             ErrorCode = 37
	ErrInvalidReplicationFactor      ErrorCode = 38
	ErrInvalidReplicaAssignment      ErrorCode = 39
	ErrInvalidConfig                 ErrorCode = 40
	ErrNotController                 ErrorCode = 41
	ErrInvalidRequest                ErrorCode = 42
	ErrUnsupportedForMessageFormat   ErrorCode = 43
	ErrPolicyViolation               ErrorCode = 44
	ErrNonEmptyGroup                 ErrorCode = 68
	ErrGroupIdNotFound               ErrorCode = 69
)

// String returns the error message
func (e ErrorCode) String() string {
	messages := map[ErrorCode]string{
		ErrNone:                      "NONE",
		ErrUnknown:                   "UNKNOWN_SERVER_ERROR",
		ErrOffsetOutOfRange:          "OFFSET_OUT_OF_RANGE",
		ErrCorruptMessage:            "CORRUPT_MESSAGE",
		ErrUnknownTopicOrPartition:   "UNKNOWN_TOPIC_OR_PARTITION",
		ErrLeaderNotAvailable:        "LEADER_NOT_AVAILABLE",
		ErrNotLeaderForPartition:     "NOT_LEADER_FOR_PARTITION",
		ErrRequestTimedOut:           "REQUEST_TIMED_OUT",
		ErrCoordinatorNotAvailable:   "COORDINATOR_NOT_AVAILABLE",
		ErrNotCoordinator:            "NOT_COORDINATOR",
		ErrInvalidTopicException:     "INVALID_TOPIC_EXCEPTION",
		ErrIllegalGeneration:         "ILLEGAL_GENERATION",
		ErrUnknownMemberId:           "UNKNOWN_MEMBER_ID",
		ErrRebalanceInProgress:       "REBALANCE_IN_PROGRESS",
		ErrTopicAlreadyExists:        "TOPIC_ALREADY_EXISTS",
		ErrInvalidPartitions:         "INVALID_PARTITIONS",
		ErrInvalidReplicationFactor:  "INVALID_REPLICATION_FACTOR",
		ErrUnsupportedVersion:        "UNSUPPORTED_VERSION",
		ErrGroupIdNotFound:           "GROUP_ID_NOT_FOUND",
	}
	if msg, ok := messages[e]; ok {
		return msg
	}
	return "UNKNOWN_ERROR"
}

// Error implements the error interface
func (e ErrorCode) Error() string {
	return e.String()
}
