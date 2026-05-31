import { parseJsonContent } from './content.js';
import {
  KINDS,
  SOUL_FACTORY_RUNTIME_CAPABILITY_SCHEMA,
  SOUL_FACTORY_RUNTIME_CONTROL_SCHEMA
} from './kinds.js';
import { getDTag, getTagValue, getTagValues, eventCoordinate } from './tags.js';

function arrayFrom(value) {
  if (!value) return [];
  return Array.isArray(value) ? value.filter((item) => item !== undefined && item !== null && item !== '') : [value];
}

function unique(values = []) {
  return Array.from(new Set(values.filter(Boolean)));
}

function normalizeRelayHints(input = {}) {
  return {
    read: arrayFrom(input.read),
    write: arrayFrom(input.write),
    control: arrayFrom(input.control)
  };
}

export function parseRuntimeCapabilityEvent(event) {
  if (!event) return null;

  const content = parseJsonContent(event, {});
  const methods = [...arrayFrom(content.methods)];
  const controllerPubkeys = [
    ...arrayFrom(content.controller_pubkeys || content.controllerPubkeys),
    ...getTagValues(event, 'controller')
  ];
  const relayHints = normalizeRelayHints(content.relay_hints || content.relayHints || {});

  for (const tag of event.tags || []) {
    if (!Array.isArray(tag) || tag.length < 2) continue;
    switch (tag[0]) {
      case 'method':
        methods.push(tag[1]);
        break;
      case 'relay': {
        const scope = tag[2] || 'control';
        if (relayHints[scope]) relayHints[scope].push(tag[1]);
        break;
      }
      case 'read-relay':
        relayHints.read.push(tag[1]);
        break;
      case 'write-relay':
        relayHints.write.push(tag[1]);
        break;
      case 'control-relay':
        relayHints.control.push(tag[1]);
        break;
    }
  }

  const runtime = getTagValue(event, 'runtime', content.runtime || getDTag(event));
  const schema = getTagValue(event, 'schema', content.schema || '');
  const controlSchema = getTagValue(event, 'control-schema', content.control_schema || content.controlSchema || '');

  return {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    identifier: getDTag(event),
    coordinate: eventCoordinate(event),
    runtime,
    schema,
    controlSchema,
    methods: unique(methods),
    controllerPubkeys: unique(controllerPubkeys),
    relayHints: {
      read: unique(relayHints.read),
      write: unique(relayHints.write),
      control: unique(relayHints.control)
    },
    content,
    event,
    compatible:
      schema === SOUL_FACTORY_RUNTIME_CAPABILITY_SCHEMA &&
      controlSchema === SOUL_FACTORY_RUNTIME_CONTROL_SCHEMA
  };
}

export function runtimeCapabilitySupports(capability, { runtime = '', method = '', controllerPubkey = '' } = {}) {
  if (!capability?.compatible) return false;
  if (runtime && capability.runtime !== runtime) return false;
  if (method && !capability.methods.includes(method)) return false;
  if (controllerPubkey && capability.controllerPubkeys.length > 0 && !capability.controllerPubkeys.includes(controllerPubkey)) {
    return false;
  }
  return true;
}
