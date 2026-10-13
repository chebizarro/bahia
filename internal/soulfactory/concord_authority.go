package soulfactory

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"

	"fiatjaf.com/nostr"
)

// A held edition can be rejected, eligible, or unresolved. Missing exact
// citations and dependency cycles must not let a lower candidate masquerade
// as the head of a complete Control Plane.
type concordEligibility uint8

const (
	concordRejected concordEligibility = iota
	concordEligible
	concordUnresolved
)

const (
	concordManageRoles    uint64 = 1 << 0
	concordManageChannels uint64 = 1 << 1
	concordManageMetadata uint64 = 1 << 2
	concordBan            uint64 = 1 << 4
	concordCreateInvite   uint64 = 1 << 6
	concordPinMessages    uint64 = 1 << 11
)

type concordRole struct {
	position    uint64
	permissions uint64
	channel     *[32]byte
}

type concordGrant struct {
	member [32]byte
	roles  [][32]byte
}

type concordPower struct {
	position uint64
	server   uint64
	channels map[[32]byte]uint64
}

type concordAuthorityHead struct {
	edition    concordEdition
	rank       uint64
	ok         bool
	fork       bool
	unresolved bool
}

type concordAuthorityResolver struct {
	community    [32]byte
	owner        nostr.PubKey
	all          map[[32]byte][]concordEdition
	memo         map[[32]byte]concordAuthorityHead
	visiting     map[[32]byte]bool
	checking     map[nostr.ID]bool
	pins         map[[32]byte][32]byte
	ignoredRoles map[[32]byte]struct{}
}

// resolveConcordControlAuthority judges every candidate against the current,
// owner-rooted roster before version or rumor-id selection. The signed seal is
// carried unchanged; exact non-head Grant editions remain in all for citations.
// Refounding stays disabled until that evidence has a supported cross-epoch
// availability contract, even when this current-epoch fold is complete.
func resolveConcordControlAuthority(raw *concordControlFold, community [32]byte, owner nostr.PubKey) *concordControlFold {
	fold := &concordControlFold{
		heads: make(map[[32]byte]concordEdition), suspended: make(map[[32]byte]struct{}),
		unresolved: make(map[[32]byte]struct{}),
	}
	if raw == nil || owner == nostr.ZeroPK {
		return fold
	}
	fold.all, fold.editions = raw.all, raw.editions
	r := &concordAuthorityResolver{
		community: community, owner: owner, all: raw.all,
		memo: make(map[[32]byte]concordAuthorityHead), visiting: make(map[[32]byte]bool),
		checking: make(map[nostr.ID]bool), pins: make(map[[32]byte][32]byte),
		ignoredRoles: make(map[[32]byte]struct{}),
	}
	// CORD-04 folds only the 100 lowest Role coordinates. Extra Roles
	// contribute no rank or permission, regardless of relay arrival order.
	var roleIDs [][32]byte
	for entity, editions := range raw.all {
		for _, edition := range editions {
			if edition.vsk == 1 {
				roleIDs = append(roleIDs, entity)
				break
			}
		}
	}
	sort.Slice(roleIDs, func(i, j int) bool { return nostr.ID(roleIDs[i]).Hex() < nostr.ID(roleIDs[j]).Hex() })
	for _, entity := range roleIDs[min(100, len(roleIDs)):] {
		r.ignoredRoles[entity] = struct{}{}
	}
	for entity, editions := range raw.all {
		for _, edition := range editions {
			if edition.vsk == 2 {
				pins, err := concordPinsLocator(community, entity)
				if err == nil {
					r.pins[pins] = entity
				}
			}
		}
	}
	for entity := range raw.all {
		result := r.head(entity)
		switch {
		case result.unresolved:
			fold.unresolved[entity] = struct{}{}
		case result.fork:
			fold.suspended[entity] = struct{}{}
		case result.ok:
			fold.heads[entity] = result.edition
		}
	}
	return fold
}

func (r *concordAuthorityResolver) head(entity [32]byte) concordAuthorityHead {
	if _, ignored := r.ignoredRoles[entity]; ignored {
		return concordAuthorityHead{}
	}
	if result, cached := r.memo[entity]; cached {
		return result
	}
	if r.visiting[entity] {
		return concordAuthorityHead{unresolved: true}
	}
	r.visiting[entity] = true
	defer delete(r.visiting, entity)
	// One coordinate cannot change entity family. A type collision must not
	// let a differently permissioned family replace a Grant or Role.
	var family uint64
	for i, edition := range r.all[entity] {
		if i == 0 {
			family = edition.vsk
		}
		if edition.vsk != family {
			result := concordAuthorityHead{unresolved: true}
			r.memo[entity] = result
			return result
		}
	}
	type ranked struct {
		edition concordEdition
		rank    uint64
	}
	var eligible []ranked
	pending := false
	for _, edition := range r.all[entity] {
		power, state := r.check(edition)
		switch state {
		case concordEligible:
			eligible = append(eligible, ranked{edition: edition, rank: power.position})
		case concordUnresolved:
			pending = true
		}
	}
	if pending {
		result := concordAuthorityHead{unresolved: true}
		r.memo[entity] = result
		return result
	}
	sort.Slice(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if a.edition.version != b.edition.version {
			return a.edition.version < b.edition.version
		}
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		return a.edition.rumorID.Hex() < b.edition.rumorID.Hex()
	})
	var chosen []ranked
	for _, candidate := range eligible {
		if n := len(chosen); n > 0 && chosen[n-1].edition.version == candidate.edition.version {
			continue
		}
		chosen = append(chosen, candidate)
	}
	result := concordAuthorityHead{}
	for i := 1; i < len(chosen); i++ {
		if chosen[i].edition.version == chosen[i-1].edition.version+1 &&
			chosen[i].edition.prev != chosen[i-1].edition.hash {
			result.fork = true
			r.memo[entity] = result
			return result
		}
	}
	if len(chosen) > 0 {
		last := chosen[len(chosen)-1]
		result = concordAuthorityHead{edition: last.edition, rank: last.rank, ok: true}
	}
	r.memo[entity] = result
	return result
}

func (r *concordAuthorityResolver) check(edition concordEdition) (concordPower, concordEligibility) {
	if r.checking[edition.rumorID] {
		return concordPower{}, concordUnresolved
	}
	r.checking[edition.rumorID] = true
	defer delete(r.checking, edition.rumorID)
	power, state := r.actorPower(edition)
	if state != concordEligible {
		return concordPower{}, state
	}
	return power, r.checkAction(edition, power)
}

func (r *concordAuthorityResolver) actorPower(edition concordEdition) (concordPower, concordEligibility) {
	if edition.actor == r.owner {
		if !edition.citation.absent() {
			return concordPower{}, concordRejected
		}
		return concordPower{position: 0, server: math.MaxUint64}, concordEligible
	}
	if edition.citation.absent() {
		return concordPower{}, concordRejected
	}
	member := [32]byte(edition.actor)
	grantID, err := concordGrantLocator(r.community, member)
	if err != nil || edition.citation.eid != nostr.ID(grantID).Hex() {
		return concordPower{}, concordRejected
	}
	matched, authorized, pending := false, false, false
	for _, candidate := range r.all[grantID] {
		if candidate.vsk != 3 || candidate.version != edition.citation.version || candidate.hash != edition.citation.hash {
			continue
		}
		matched = true
		_, state := r.check(candidate)
		if state == concordEligible {
			authorized = true
		}
		if state == concordUnresolved {
			pending = true
		}
	}
	if !matched || pending {
		return concordPower{}, concordUnresolved
	}
	if !authorized {
		return concordPower{}, concordRejected
	}
	current := r.head(grantID)
	if current.unresolved || current.fork || !current.ok || current.edition.version < edition.citation.version {
		return concordPower{}, concordUnresolved
	}
	grant, ok := parseConcordGrant(current.edition, r.community)
	if !ok || grant.member != member {
		return concordPower{}, concordRejected
	}
	power := concordPower{position: math.MaxUint64, channels: make(map[[32]byte]uint64)}
	for _, roleID := range grant.roles {
		if _, ignored := r.ignoredRoles[roleID]; ignored {
			continue
		}
		roleHead := r.head(roleID)
		if roleHead.unresolved || roleHead.fork || !roleHead.ok {
			return concordPower{}, concordUnresolved
		}
		role, valid := parseConcordRole(roleHead.edition)
		if !valid {
			return concordPower{}, concordRejected
		}
		if role.position < power.position {
			power.position = role.position
		}
		if role.channel == nil {
			power.server |= role.permissions
		} else {
			power.channels[*role.channel] |= role.permissions
		}
	}
	return power, concordEligible
}

func (r *concordAuthorityResolver) checkAction(edition concordEdition, actor concordPower) concordEligibility {
	var bit uint64
	var channel *[32]byte
	switch edition.vsk {
	case 0:
		if edition.entity != r.community {
			return concordRejected
		}
		bit = concordManageMetadata
	case 1:
		bit = concordManageRoles
		role, ok := parseConcordRole(edition)
		if !ok || actor.position >= role.position {
			return concordRejected
		}
		if state := r.priorRoleRank(edition, actor.position); state != concordEligible {
			return state
		}
	case 2:
		bit = concordManageChannels
		channel = &edition.entity
		prior, state := r.predecessor(edition)
		if state != concordEligible {
			return state
		}
		if prior != nil {
			var metadata struct {
				Deleted bool `json:"deleted"`
			}
			if json.Unmarshal([]byte(prior.content), &metadata) != nil {
				return concordRejected
			}
			if metadata.Deleted {
				return concordRejected
			}
		}
	case 3:
		bit = concordManageRoles
		grant, ok := parseConcordGrant(edition, r.community)
		if !ok {
			return concordRejected
		}
		for _, roleID := range grant.roles {
			if _, ignored := r.ignoredRoles[roleID]; ignored {
				continue
			}
			roleHead := r.head(roleID)
			if roleHead.unresolved || roleHead.fork || !roleHead.ok {
				return concordUnresolved
			}
			role, valid := parseConcordRole(roleHead.edition)
			if !valid || actor.position >= role.position {
				return concordRejected
			}
		}
		if state := r.priorGrantRank(edition, actor.position); state != concordEligible {
			return state
		}
	case 4:
		bit = concordBan
		coordinate, err := concordBanlistLocator(r.community)
		if err != nil || edition.entity != coordinate || !concordHexList(edition.content) {
			return concordRejected
		}
	case 8:
		bit = concordCreateInvite
		coordinate, err := concordInviteLinksLocator(r.community, [32]byte(edition.actor))
		if err != nil || edition.entity != coordinate || !concordHexList(edition.content) {
			return concordRejected
		}
	case 11:
		bit = concordPinMessages
		id, ok := r.pins[edition.entity]
		if !ok {
			return concordUnresolved
		}
		channelHead := r.head(id)
		if channelHead.unresolved || channelHead.fork || !channelHead.ok {
			return concordUnresolved
		}
		if channelHead.edition.vsk != 2 {
			return concordRejected
		}
		channel = &id
	default:
		return concordRejected
	}
	if edition.actor == r.owner {
		return concordEligible
	}
	bits := actor.server
	if channel != nil {
		bits |= actor.channels[*channel]
	}
	if bits&bit == 0 {
		return concordRejected
	}
	return concordEligible
}

// When the predecessor is held, a staff editor must outrank the old target as
// well as the new target. A pruned predecessor is not proof of old rank; such
// staff edits defer, while an owner can always establish the fresh-join floor.
func (r *concordAuthorityResolver) predecessor(edition concordEdition) (*concordEdition, concordEligibility) {
	if edition.version == 1 {
		return nil, concordEligible
	}
	var authorized *concordEdition
	found, pending := false, false
	for i := range r.all[edition.entity] {
		candidate := &r.all[edition.entity][i]
		if candidate.version == edition.version-1 && candidate.hash == edition.prev {
			found = true
			_, state := r.check(*candidate)
			switch state {
			case concordUnresolved:
				pending = true
			case concordEligible:
				authorized = candidate
			}
		}
	}
	if pending {
		return nil, concordUnresolved
	}
	if authorized != nil {
		return authorized, concordEligible
	}
	if found {
		return nil, concordRejected
	}
	if edition.actor == r.owner {
		return nil, concordEligible
	}
	return nil, concordUnresolved
}

func (r *concordAuthorityResolver) priorRoleRank(edition concordEdition, rank uint64) concordEligibility {
	prior, state := r.predecessor(edition)
	if state != concordEligible || prior == nil {
		return state
	}
	role, ok := parseConcordRole(*prior)
	if !ok || rank >= role.position {
		return concordRejected
	}
	return concordEligible
}

func (r *concordAuthorityResolver) priorGrantRank(edition concordEdition, rank uint64) concordEligibility {
	prior, state := r.predecessor(edition)
	if state != concordEligible || prior == nil {
		return state
	}
	grant, ok := parseConcordGrant(*prior, r.community)
	if !ok {
		return concordRejected
	}
	for _, roleID := range grant.roles {
		if _, ignored := r.ignoredRoles[roleID]; ignored {
			continue
		}
		roleHead := r.head(roleID)
		if roleHead.unresolved || roleHead.fork || !roleHead.ok {
			return concordUnresolved
		}
		role, valid := parseConcordRole(roleHead.edition)
		if !valid || rank >= role.position {
			return concordRejected
		}
	}
	return concordEligible
}

func parseConcordRole(edition concordEdition) (concordRole, bool) {
	if edition.vsk != 1 {
		return concordRole{}, false
	}
	var wire struct {
		RoleID      string          `json:"role_id"`
		Name        string          `json:"name"`
		Position    uint32          `json:"position"`
		Permissions json.RawMessage `json:"permissions"`
		Scope       struct {
			Kind      string `json:"kind"`
			ChannelID string `json:"channel_id"`
		} `json:"scope"`
		Color uint32 `json:"color"`
	}
	if json.Unmarshal([]byte(edition.content), &wire) != nil || wire.RoleID != nostr.ID(edition.entity).Hex() ||
		len(wire.Name) > 64 || wire.Position == 0 || len(wire.Permissions) == 0 {
		return concordRole{}, false
	}
	var decimal string
	if wire.Permissions[0] == '"' {
		if json.Unmarshal(wire.Permissions, &decimal) != nil {
			return concordRole{}, false
		}
	} else {
		decimal = string(wire.Permissions)
	}
	if decimal == "" || (len(decimal) > 1 && decimal[0] == '0') {
		return concordRole{}, false
	}
	permissions, err := strconv.ParseUint(decimal, 10, 64)
	if err != nil {
		return concordRole{}, false
	}
	role := concordRole{position: uint64(wire.Position), permissions: permissions}
	switch wire.Scope.Kind {
	case "server":
		if wire.Scope.ChannelID != "" {
			return concordRole{}, false
		}
	case "channel":
		id, err := concordID32(wire.Scope.ChannelID)
		if err != nil {
			return concordRole{}, false
		}
		role.channel = &id
	default:
		return concordRole{}, false
	}
	return role, true
}

func parseConcordGrant(edition concordEdition, community [32]byte) (concordGrant, bool) {
	if edition.vsk != 3 {
		return concordGrant{}, false
	}
	var wire struct {
		Member      string   `json:"member"`
		RoleIDs     []string `json:"role_ids"`
		ControlWrap string   `json:"control_wrap"`
	}
	if json.Unmarshal([]byte(edition.content), &wire) != nil || wire.RoleIDs == nil || len(wire.RoleIDs) > 64 {
		return concordGrant{}, false
	}
	member, err := concordID32(wire.Member)
	if err != nil {
		return concordGrant{}, false
	}
	coordinate, err := concordGrantLocator(community, member)
	if err != nil || edition.entity != coordinate {
		return concordGrant{}, false
	}
	grant := concordGrant{member: member, roles: make([][32]byte, 0, len(wire.RoleIDs))}
	seen := make(map[[32]byte]struct{}, len(wire.RoleIDs))
	for _, value := range wire.RoleIDs {
		role, err := concordID32(value)
		if err != nil {
			return concordGrant{}, false
		}
		if _, duplicate := seen[role]; duplicate {
			return concordGrant{}, false
		}
		seen[role] = struct{}{}
		grant.roles = append(grant.roles, role)
	}
	return grant, true
}

func concordHexList(content string) bool {
	var values []string
	if json.Unmarshal([]byte(content), &values) != nil || values == nil {
		return false
	}
	for _, value := range values {
		if _, err := concordID32(value); err != nil {
			return false
		}
	}
	return true
}
