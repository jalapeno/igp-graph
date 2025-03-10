# igp-graph

## Description

igp-graph is a Jalapeno data processor service that compiles existing link-state data from Arango documents collections and creates an both a complete IGP graph for both IPv4 and IPv6 (igpv4_graph and igpv6_graph respectfully). The processor also subcribes to bgp-ls events topics on Kafka topics and inserts or deletes entries in the graph collections accordingly.

## Prerequisites
* Kubernetes cluster
* Jalapeno platform installed [Jalapeno Platform](https://github.com/cisco-open/jalapeno)

