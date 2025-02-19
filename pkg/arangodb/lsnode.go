package arangodb

import (
	"context"
	"strconv"

	driver "github.com/arangodb/go-driver"
	"github.com/golang/glog"
	"github.com/sbezverk/gobmp/pkg/base"
	"github.com/sbezverk/gobmp/pkg/message"
)

// Add srv6 sids / locators to nodes in the igp_node collection
func (a *arangoDB) processLSSRv6SID(ctx context.Context, key, id string, e *message.LSSRv6SID) error {
	query := "for l in " + a.igpNode.Name() +
		" filter l.igp_router_id == " + "\"" + e.IGPRouterID + "\"" +
		" filter l.domain_id == " + strconv.Itoa(int(e.DomainID))
	query += " return l"
	ncursor, err := a.db.Query(ctx, query, nil)
	if err != nil {
		return err
	}
	defer ncursor.Close()
	var sn igpNode
	ns, err := ncursor.ReadDocument(ctx, &sn)
	if err != nil {
		if !driver.IsNoMoreDocuments(err) {
			return err
		}
	}
	glog.Infof("igp_node %s + srv6sid %s", ns.Key, e.SRv6SID)
	//glog.Infof("existing sids: %+v", &sn.SIDS)

	newsid := SID{
		SRv6SID:              e.SRv6SID,
		SRv6EndpointBehavior: e.SRv6EndpointBehavior,
		SRv6BGPPeerNodeSID:   e.SRv6BGPPeerNodeSID,
		SRv6SIDStructure:     e.SRv6SIDStructure,
	}
	var result bool = false
	for _, x := range sn.SIDS {
		if x == newsid {
			result = true
			break
		}
	}
	if result {
		glog.Infof("sid %+v exists in igp_node document", e.SRv6SID)
	} else {

		sn.SIDS = append(sn.SIDS, newsid)
		srn := igpNode{
			SIDS: sn.SIDS,
		}
		if _, err := a.igpNode.UpdateDocument(ctx, ns.Key, &srn); err != nil {
			if !driver.IsConflict(err) {
				return err
			}
		}
	}
	return nil
}

// Find and add sr-mpls prefix sids to nodes in the igp_node collection
func (a *arangoDB) processPrefixSID(ctx context.Context, key, id string, e message.LSPrefix) error {
	query := "for l in  " + a.igpNode.Name() +
		" filter l.igp_router_id == " + "\"" + e.IGPRouterID + "\""
	query += " return l"
	pcursor, err := a.db.Query(ctx, query, nil)
	if err != nil {
		return err
	}
	defer pcursor.Close()
	for {
		var ln igpNode
		nl, err := pcursor.ReadDocument(ctx, &ln)
		if err != nil {
			if !driver.IsNoMoreDocuments(err) {
				return err
			}
			break
		}
		glog.V(6).Infof("igp_node: %s + prefix sid %v +  ", ln.Key, e.PrefixAttrTLVs.LSPrefixSID)

		obj := srObject{
			PrefixAttrTLVs: e.PrefixAttrTLVs,
		}

		if _, err := a.igpNode.UpdateDocument(ctx, nl.Key, &obj); err != nil {
			if !driver.IsConflict(err) {
				return err
			}
		}
	}
	return nil
}

// Find and add ls_node entries to the igp_node collection
func (a *arangoDB) processIgpNode(ctx context.Context, key string, e *message.LSNode) error {
	if e.ProtocolID == base.BGP {
		// EPE Case cannot be processed because LS Node collection does not have BGP routers
		return nil
	}
	query := "for l in " + a.lsnode.Name() +
		" filter l._key == " + "\"" + e.Key + "\""
	query += " return l"
	ncursor, err := a.db.Query(ctx, query, nil)
	if err != nil {
		return err
	}
	defer ncursor.Close()
	var sn igpNode
	ns, err := ncursor.ReadDocument(ctx, &sn)
	if err != nil {
		if !driver.IsNoMoreDocuments(err) {
			return err
		}
	}

	if _, err := a.igpNode.CreateDocument(ctx, &sn); err != nil {
		glog.Infof("adding igp_node: %s with area_id %s ", sn.Key, e.AreaID)
		if !driver.IsConflict(err) {
			return err
		}
		if err := a.findPrefixSID(ctx, sn.Key, e); err != nil {
			if err != nil {
				return err
			}
		}
		// The document already exists, updating it with the latest info
		if _, err := a.igpNode.UpdateDocument(ctx, ns.Key, e); err != nil {
			return err
		}
		return nil
	}

	if err := a.processIgpNode(ctx, ns.Key, e); err != nil {
		glog.Errorf("Failed to process igp_node %s with error: %+v", ns.Key, err)
	}

	if err := a.processIgpDomain(ctx, ns.Key, e); err != nil {
		if err != nil {
			return err
		}
	}
	return nil
}

// Find sr-mpls prefix sids and add them to newly added node igp_node collection
func (a *arangoDB) findPrefixSID(ctx context.Context, key string, e *message.LSNode) error {
	query := "for l in " + a.lsprefix.Name() +
		" filter l.igp_router_id == " + "\"" + e.IGPRouterID + "\"" +
		" filter l.prefix_attr_tlvs.ls_prefix_sid != null"
	query += " return l"
	ncursor, err := a.db.Query(ctx, query, nil)
	if err != nil {
		return err
	}
	defer ncursor.Close()
	var lp message.LSPrefix
	pl, err := ncursor.ReadDocument(ctx, &lp)
	if err != nil {
		if !driver.IsNoMoreDocuments(err) {
			return err
		}
	}
	obj := srObject{
		PrefixAttrTLVs: lp.PrefixAttrTLVs,
	}
	if _, err := a.igpNode.UpdateDocument(ctx, e.Key, &obj); err != nil {
		glog.V(5).Infof("adding prefix sid: %s ", pl.Key)
		return err
	}
	if err := a.dedupeIgpNode(); err != nil {
		if err != nil {
			return err
		}
	}
	return nil
}

// BGP-LS generates a level-1 and a level-2 entry for level-1-2 nodes
// remove duplicate entries in the igp_node collection
func (a *arangoDB) dedupeIgpNode() error {
	ctx := context.TODO()
	dup_query := "LET duplicates = ( FOR d IN " + a.igpNode.Name() +
		" COLLECT id = d.igp_router_id, domain = d.domain_id WITH COUNT INTO count " +
		" FILTER count > 1 RETURN { id: id, domain: domain, count: count }) " +
		"FOR d IN duplicates FOR m IN igp_node " +
		"FILTER d.id == m.igp_router_id filter d.domain == m.domain_id RETURN m "
	pcursor, err := a.db.Query(ctx, dup_query, nil)
	glog.Infof("dedup query: %+v", dup_query)
	if err != nil {
		return err
	}
	defer pcursor.Close()
	for {
		var doc duplicateNode
		dupe, err := pcursor.ReadDocument(ctx, &doc)

		if err != nil {
			if !driver.IsNoMoreDocuments(err) {
				return err
			}
			break
		}
		glog.Infof("Got doc with key '%s' from query\n", dupe.Key)

		if doc.ProtocolID == 1 {
			glog.Infof("remove level-1 duplicate node: %s + igp id: %s protocol id: %v +  ", doc.Key, doc.IGPRouterID, doc.ProtocolID)
			if _, err := a.igpNode.RemoveDocument(ctx, doc.Key); err != nil {
				if !driver.IsConflict(err) {
					return err
				}
			}
		}
		if doc.ProtocolID == 2 {
			update_query := "for l in " + a.igpNode.Name() + " filter l._key == " + "\"" + doc.Key + "\"" +
				" UPDATE l with { protocol: " + "\"" + "ISIS Level 1-2" + "\"" + " } in " + a.igpNode.Name() + ""
			cursor, err := a.db.Query(ctx, update_query, nil)
			glog.Infof("update query: %s ", update_query)
			if err != nil {
				return err
			}
			defer cursor.Close()
		}
	}
	return nil
}

// Nov 10 2024 - find ipv6 lsnode's ipv4 bgp router-id
func (a *arangoDB) processbgp6(ctx context.Context, key, id string, e *message.PeerStateChange) error {
	query := "for l in  " + a.igpNode.Name() +
		" filter l.router_id == " + "\"" + e.RemoteBGPID + "\""
	query += " return l"
	pcursor, err := a.db.Query(ctx, query, nil)
	if err != nil {
		return err
	}
	defer pcursor.Close()
	for {
		var ln igpNode
		nl, err := pcursor.ReadDocument(ctx, &ln)
		if err != nil {
			if !driver.IsNoMoreDocuments(err) {
				return err
			}
			break
		}
		//glog.Infof("igp_node: %s + peer %v +  ", ln.Key, e.RemoteBGPID)

		obj := peerObject{
			BGPRouterID: e.RemoteBGPID,
		}
		glog.Infof("igp_node: %s + peer %v +  object: %+v", nl.Key, e.RemoteBGPID, obj)
		if _, err := a.igpNode.UpdateDocument(ctx, nl.Key, &obj); err != nil {
			if !driver.IsConflict(err) {
				return err
			}
		}
	}
	return nil
}

// processIgpNodeRemoval removes records from the igp_node collection which are referring to deleted LSNode
func (a *arangoDB) processIgpNodeRemoval(ctx context.Context, key string) error {
	query := "FOR d IN " + a.igpNode.Name() +
		" filter d._key == " + "\"" + key + "\""
	query += " return d"
	ncursor, err := a.db.Query(ctx, query, nil)
	if err != nil {
		return err
	}
	defer ncursor.Close()

	for {
		var nm igpNode
		m, err := ncursor.ReadDocument(ctx, &nm)
		if err != nil {
			if !driver.IsNoMoreDocuments(err) {
				return err
			}
			break
		}
		if _, err := a.igpNode.RemoveDocument(ctx, m.ID.Key()); err != nil {
			if !driver.IsNotFound(err) {
				return err
			}
		}
	}

	return nil
}

// when a new igp domain is detected, create a new entry in the igp_domain collection
func (a *arangoDB) processIgpDomain(ctx context.Context, key string, e *message.LSNode) error {
	if e.ProtocolID == base.BGP {
		// EPE Case cannot be processed because LS Node collection does not have BGP routers
		return nil
	}
	query := "for l in igp_node insert " +
		"{ _key: CONCAT_SEPARATOR(" + "\"_\", l.protocol_id, l.domain_id, l.asn), " +
		"asn: l.asn, protocol_id: l.protocol_id, domain_id: l.domain_id, protocol: l.protocol } " +
		"into igp_domain OPTIONS { ignoreErrors: true } "
	query += " return l"
	ncursor, err := a.db.Query(ctx, query, nil)
	if err != nil {
		return err
	}
	defer ncursor.Close()
	var sn igpNode
	ns, err := ncursor.ReadDocument(ctx, &sn)
	if err != nil {
		if !driver.IsNoMoreDocuments(err) {
			return err
		}
	}

	if _, err := a.igpDomain.CreateDocument(ctx, &sn); err != nil {
		glog.Infof("adding igp_domain: %s with area_id %v ", sn.Key, e.ASN)
		if !driver.IsConflict(err) {
			return err
		}
		if err := a.findPrefixSID(ctx, sn.Key, e); err != nil {
			if err != nil {
				return err
			}
		}
		// The document already exists, updating it with the latest info
		if _, err := a.igpDomain.UpdateDocument(ctx, ns.Key, e); err != nil {
			return err
		}
		return nil
	}
	if err := a.processIgpDomain(ctx, ns.Key, e); err != nil {
		glog.Errorf("Failed to process igp_domain %s with error: %+v", ns.Key, err)
	}
	return nil
}
