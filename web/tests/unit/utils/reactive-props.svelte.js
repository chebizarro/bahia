// Components mounted with a $state proxy receive live prop updates, which lets
// DOM specs model canonical projections changing underneath a rendered card.
export function reactiveProps(initial) {
  const props = $state(initial);
  return props;
}
