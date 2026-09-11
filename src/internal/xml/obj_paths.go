package xml

import "strings"

// Media pointers for an item.
//
// objID names an object on a device; objPaths says where the bytes are. A rundown display needs the
// thumbnail, a preview needs the proxy, and a playout or bridge needs the essence — none of which can be
// derived from objID.
//
// The ObjPaths and ObjPath types already existed for the object family in object_messages.go. What was
// missing was any reference to them from the ITEM: ItemInfo declared a bare `objPath` string, while the
// specification nests the paths one level down.
//
//	<!ELEMENT objPaths (objPath*, objProxyPath*, objMetadataPath*)>
//
// So the bare tag never matched the real shape and every pointer was dropped: 57 captured frames carried
// objPaths and not one URL was stored (doc/interop §49). The same class of defect as the nested mosItem in
// §40 and §45 — a structure modelled correctly at one end of the codebase and unreferenced at the other.
//
// Note the plurality. objPath and objProxyPath are BOTH repeatable, and a real rundown uses that: one
// essence path plus separate proxy entries for a low-resolution preview and a still thumbnail. A
// single-valued field would silently keep whichever came last.

// Empty reports whether no pointer of any kind is present.
func (o *ObjPaths) Empty() bool {
	if o == nil {
		return true
	}
	return len(o.ObjPath) == 0 && len(o.ObjProxyPath) == 0 && len(o.ObjMetadataPath) == 0
}

// Essence returns the first essence path, or empty when none is offered.
//
// First rather than best: the specification defines no ordering or preference among repeated paths, so
// choosing between them on any other basis would invent a rule the sender never agreed to.
func (o *ObjPaths) Essence() string {
	if o == nil || len(o.ObjPath) == 0 {
		return ""
	}
	return strings.TrimSpace(o.ObjPath[0].Value)
}

// Proxy returns the first proxy path, or empty when none is offered.
func (o *ObjPaths) Proxy() string {
	if o == nil || len(o.ObjProxyPath) == 0 {
		return ""
	}
	return strings.TrimSpace(o.ObjProxyPath[0].Value)
}

// Metadata returns the first object-metadata path, or empty when none is offered.
func (o *ObjPaths) Metadata() string {
	if o == nil || len(o.ObjMetadataPath) == 0 {
		return ""
	}
	return strings.TrimSpace(o.ObjMetadataPath[0].Value)
}

// ProxyMatching returns the first proxy whose techDescription contains want, case-insensitively, falling
// back to the first proxy of any kind.
//
// A real rundown distinguishes its proxies only by that free-text attribute: one captured item offers
// techDescription "Proxy" for a video preview and "JPG" for a still. A caller wanting a thumbnail has
// nothing else to go on, so substring matching is the honest way to express a preference the protocol does
// not model — and falling back rather than returning nothing keeps a caller working against a sender that
// labels its proxies differently.
//
// techDescription is REQUIRED by the specification, and a live NCS nonetheless sends it EMPTY on the
// essence path while populating it on the proxies. So it is treated as advisory: never used to decide
// whether a path is usable, only to choose between paths that are.
func (o *ObjPaths) ProxyMatching(want string) string {
	if o == nil {
		return ""
	}
	if want != "" {
		lowerWant := strings.ToLower(want)
		for _, p := range o.ObjProxyPath {
			if strings.Contains(strings.ToLower(p.TechDescription), lowerWant) {
				return strings.TrimSpace(p.Value)
			}
		}
	}
	return o.Proxy()
}
