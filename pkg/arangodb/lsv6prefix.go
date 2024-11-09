package arangodb

import (
	"context"
	"fmt"
	"strconv"

	driver "github.com/arangodb/go-driver"
	"github.com/golang/glog"
	"github.com/sbezverk/gobmp/pkg/base"
	"github.com/sbezverk/gobmp/pkg/message"
)

// func (a *arangoDB) lsprefixHandler(obj *kafkanotifier.EventMessage) error {
// 	ctx := context.TODO()
// 	if obj == nil {
// 		return fmt.Errorf("event message is nil")
// 	}
// 	// Check if Collection encoded in ID exists
// 	c := strings.Split(obj.ID, "/")[0]
// 	if strings.Compare(c, a.lsprefix.Name()) != 0 {
// 		return fmt.Errorf("configured collection name %s and received in event collection name %s do not match", a.lsprefix.Name(), c)
// 	}
// 	//glog.V(5).Infof("Processing action: %s for key: %s ID: %s", obj.Action, obj.Key, obj.ID)
// 	var o message.LSPrefix
// 	_, err := a.lsprefix.ReadDocument(ctx, obj.Key, &o)
// 	if err != nil {
// 		// In case of a ls_link removal notification, reading it will return Not Found error
// 		if !driver.IsNotFound(err) {
// 			return fmt.Errorf("failed to read existing document %s with error: %+v", obj.Key, err)
// 		}
// 		// If operation matches to "del" then it is confirmed delete operation, otherwise return error
// 		if obj.Action != "del" {
// 			return fmt.Errorf("document %s not found but Action is not \"del\", possible stale event", obj.Key)
// 		}
// 		err := a.processv6PrefixRemoval(ctx, obj.Key, obj.Action)
// 		if err != nil {
// 			return err
// 		}
// 		// write event into ls_node_edge topic
// 		a.notifier.EventNotification(obj)
// 		return nil
// 	}
// 	switch obj.Action {
// 	case "add":
// 		fallthrough
// 	case "update":
// 		if err := a.processLSv6PrefixEdge(ctx, obj.Key, &o); err != nil {
// 			return fmt.Errorf("failed to process action %s for edge %s with error: %+v", obj.Action, obj.Key, err)
// 		}
// 	}
// 	//glog.V(5).Infof("Complete processing action: %s for key: %s ID: %s", obj.Action, obj.Key, obj.ID)

// 	// write event into ls_topoogy_v4 topic
// 	a.notifier.EventNotification(obj)

// 	return nil
// }

// processEdge processes a single ls_link connection which is a unidirectional edge between two nodes (vertices).
func (a *arangoDB) processLSv6PrefixEdge(ctx context.Context, key string, p *message.LSPrefix) error {
	//glog.V(9).Infof("processEdge processing lsprefix: %s", l.ID)

	// filter out IPv6, ls link, and loopback prefixes
	if p.MTID == nil || p.PrefixLen == 126 || p.PrefixLen == 127 || p.PrefixLen == 128 {
		return nil
	}

	// get remote node from ls_link entry
	lsnode, err := a.getLSv6Node(ctx, p, false)
	if err != nil {
		glog.Errorf("processEdge failed to get remote lsnode %s for link: %s with error: %+v", p.IGPRouterID, p.ID, err)
		return err
	}
	if err := a.createLSv6PrefixEdgeObject(ctx, p, lsnode); err != nil {
		glog.Errorf("processEdge failed to create Edge object with error: %+v", err)
		return err
	}
	//glog.V(9).Infof("processEdge completed processing lsprefix: %s for ls nodes: %s - %s", l.ID, ln.ID, rn.ID)

	return nil
}

// processEdgeRemoval removes a record from Node's graph collection
// since the key matches in both collections (LS Links and Nodes' Graph) deleting the record directly.
func (a *arangoDB) processv6PrefixRemoval(ctx context.Context, key string, action string) error {
	if _, err := a.graphv6.RemoveDocument(ctx, key); err != nil {
		glog.Infof("removing edge %s", key)
		if !driver.IsNotFound(err) {
			return err
		}
		return nil
	}

	return nil
}

func (a *arangoDB) getLSv6Node(ctx context.Context, p *message.LSPrefix, local bool) (*message.LSNode, error) {
	// Need to find ls_node object matching ls_link's IGP Router ID
	query := "FOR d IN ls_node_extended" //+ a.lsnodeExt.Name()
	query += " filter d.igp_router_id == " + "\"" + p.IGPRouterID + "\""
	query += " filter d.domain_id == " + strconv.Itoa(int(p.DomainID))

	// If OSPFv2 or OSPFv3, then query must include AreaID
	if p.ProtocolID == base.OSPFv2 || p.ProtocolID == base.OSPFv3 {
		query += " filter d.area_id == " + "\"" + p.AreaID + "\""
	}
	query += " return d"
	//glog.Infof("query: %s", query)
	lcursor, err := a.db.Query(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	defer lcursor.Close()
	var ln message.LSNode
	i := 0
	for ; ; i++ {
		_, err := lcursor.ReadDocument(ctx, &ln)
		if err != nil {
			if !driver.IsNoMoreDocuments(err) {
				return nil, err
			}
			break
		}
	}
	if i == 0 {
		return nil, fmt.Errorf("query %s returned 0 results", query)
	}
	if i > 1 {
		return nil, fmt.Errorf("query %s returned more than 1 result", query)
	}

	return &ln, nil
}

func (a *arangoDB) createLSv6PrefixEdgeObject(ctx context.Context, l *message.LSPrefix, ln *message.LSNode) error {
	mtid := 0
	if l.MTID != nil {
		mtid = int(l.MTID.MTID)
	}
	ne := lsTopologyObject{
		Key:            l.Key,
		From:           ln.ID,
		To:             l.ID,
		Link:           l.Key,
		ProtocolID:     l.ProtocolID,
		DomainID:       l.DomainID,
		MTID:           uint16(mtid),
		AreaID:         l.AreaID,
		Protocol:       l.Protocol,
		LocalNodeASN:   ln.ASN,
		Prefix:         l.Prefix,
		PrefixLen:      l.PrefixLen,
		PrefixMetric:   l.PrefixMetric,
		PrefixAttrTLVs: l.PrefixAttrTLVs,
	}
	if _, err := a.graphv6.CreateDocument(ctx, &ne); err != nil {
		if !driver.IsConflict(err) {
			return err
		}
		// The document already exists, updating it with the latest info
		if _, err := a.graphv6.UpdateDocument(ctx, ne.Key, &ne); err != nil {
			return err
		}
	}

	return nil
}
