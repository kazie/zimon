// "/" jumps to the search box, unless you are already typing somewhere.
document.addEventListener("keydown", (e) => {
  const typing = e.target.closest("input, select, textarea, [contenteditable]");
  if (e.key === "/" && !typing && !e.ctrlKey && !e.metaKey && !e.altKey) {
    e.preventDefault();
    document.querySelector(".search input[name=pattern]").focus();
  }
});
