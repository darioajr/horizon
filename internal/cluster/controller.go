package cluster

import (
	"log"
	"sort"
)

// ---------------------------------------------------------------------------
// Controller election
// ---------------------------------------------------------------------------

// electController picks the alive node with the lowest node ID as the
// controller.  This is deterministic: every node that has the same gossip
// view will elect the same controller without any coordination.
func (c *Cluster) electController() {
	alive := c.state.AliveNodes()
	if len(alive) == 0 {
		return
	}
	sort.Slice(alive, func(i, j int) bool { return alive[i].ID < alive[j].ID })
	newCtrl := alive[0].ID
	oldCtrl := c.state.ControllerID()
	if newCtrl != oldCtrl {
		c.state.SetControllerID(newCtrl)
		log.Printf("[cluster] controller elected: node %d (was %d)", newCtrl, oldCtrl)
	}

	// If we are the new controller, compute and broadcast assignments.
	if newCtrl == c.cfg.NodeID {
		c.computeAndBroadcastAssignments()
	}
}

// ---------------------------------------------------------------------------
// Partition assignment algorithm
// ---------------------------------------------------------------------------

// computeAndBroadcastAssignments is run by the controller.  It assigns
// every topic-partition to nodes using a round-robin strategy that spreads
// replicas evenly.
func (c *Cluster) computeAndBroadcastAssignments() {
	alive := c.state.AliveNodes()
	if len(alive) == 0 {
		return
	}
	sort.Slice(alive, func(i, j int) bool { return alive[i].ID < alive[j].ID })
	nodeIDs := make([]int32, len(alive))
	for i, n := range alive {
		nodeIDs[i] = n.ID
	}

	// Get all topics from the local broker
	topics := c.localBroker.ListTopics()

	table := make(map[string]map[int32]*PartitionAssignment)

	for _, tc := range topics {
		numPartitions := tc.NumPartitions
		rf := int(tc.ReplicationFactor)
		if rf <= 0 {
			rf = int(c.cfg.ReplicationFactor)
		}
		if rf > len(nodeIDs) {
			rf = len(nodeIDs)
		}

		table[tc.Name] = make(map[int32]*PartitionAssignment)

		for p := int32(0); p < numPartitions; p++ {
			a := assignPartition(tc.Name, p, nodeIDs, rf)
			// Preserve leader epoch from existing assignment if the leader
			// hasn't changed.
			if existing := c.state.GetAssignment(tc.Name, p); existing != nil {
				if existing.Leader == a.Leader {
					a.LeaderEpoch = existing.LeaderEpoch
				} else {
					a.LeaderEpoch = existing.LeaderEpoch + 1
				}
			}
			table[tc.Name][p] = a
		}
	}

	ver := c.state.BumpVersion()
	ctrlID := c.cfg.NodeID

	// Apply locally
	c.state.SetAssignments(table, ctrlID, ver)
	log.Printf("[controller] computed assignments v%d for %d topics, broadcasting",
		ver, len(table))

	// Broadcast to all other alive nodes via RPC
	payload := EncodeAssignments(table, ctrlID, ver)
	for _, n := range alive {
		if n.ID == c.cfg.NodeID {
			continue
		}
		go func(nodeID int32) {
			client, err := c.rpcPool.Get(nodeID)
			if err != nil {
				log.Printf("[controller] cannot reach node %d for assignment: %v", nodeID, err)
				return
			}
			resp, err := client.Call(rpcAssignBroadcast, payload)
			if err != nil {
				log.Printf("[controller] assignment broadcast to node %d failed: %v", nodeID, err)
				c.rpcPool.Remove(nodeID)
				return
			}
			if len(resp) > 0 && resp[0] != rpcErrNone {
				log.Printf("[controller] node %d rejected assignment: errCode=%d", nodeID, resp[0])
			}
		}(n.ID)
	}

	// Notify replicator
	if c.replicator != nil {
		c.replicator.Refresh()
	}
}

// assignPartition assigns a single partition to nodes.
//
// Leader = nodeIDs[ partition % len(nodeIDs) ]
// Replicas = leader + next (rf-1) nodes in the ring.
func assignPartition(topic string, partition int32, nodeIDs []int32, rf int) *PartitionAssignment {
	n := len(nodeIDs)
	idx := int(partition) % n
	replicas := make([]int32, rf)
	for i := 0; i < rf; i++ {
		replicas[i] = nodeIDs[(idx+i)%n]
	}
	return &PartitionAssignment{
		Topic:       topic,
		Partition:   partition,
		Leader:      replicas[0],
		Replicas:    replicas,
		ISR:         append([]int32(nil), replicas...), // initially all replicas are in-sync
		LeaderEpoch: 0,
	}
}

// onMemberChange is called by gossip when the set of alive nodes changes.
func (c *Cluster) onMemberChange() {
	c.electController()
}
