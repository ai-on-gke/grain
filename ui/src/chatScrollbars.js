// nameScrollbars gives the chat's scrollbar thumbs the semantics of what
// they are (grain/task-2961).
//
// @chatscope/chat-ui-kit-react scrolls its message list -- and the
// composer, once a draft outgrows it -- with a vendored perfect-scrollbar,
// which draws its own thumbs and puts each one in the tab order
// (tabindex="0") as a bare <div>: no role, no name. Tab through a session
// and one stop between the last "Copy response" and the message box
// announces nothing at all. It cannot simply be taken out of the tab order
// either: the container itself is overflow:hidden and not focusable, so a
// focused thumb is the only way a keyboard reaches PageUp/PageDown/arrows
// on the transcript (perfect-scrollbar's keydown handler scrolls when its
// thumb has focus).
//
// So each thumb is made a real ARIA scrollbar: named after what it
// scrolls, oriented, pointing at its container, and carrying where in it
// the view sits. Called after every render and on every scroll, since the
// library can replace these elements and React never manages them.
export function nameScrollbars(root, label) {
  if (!root) return;
  root.querySelectorAll(".ps__thumb-y, .ps__thumb-x").forEach((thumb, i) => {
    const container = thumb.closest(".ps");
    if (!container) return;
    const vertical = thumb.classList.contains("ps__thumb-y");
    if (!container.id) container.id = `${root.id || "chat"}-scroll-${i}`;
    const inComposer = Boolean(container.closest(".cs-message-input"));
    if (thumb.getAttribute("role") !== "scrollbar") {
      thumb.setAttribute("role", "scrollbar");
      thumb.setAttribute("aria-label", inComposer ? "Scroll message" : label);
      thumb.setAttribute(
        "aria-orientation",
        vertical ? "vertical" : "horizontal",
      );
      thumb.setAttribute("aria-controls", container.id);
      thumb.setAttribute("aria-valuemin", "0");
      thumb.setAttribute("aria-valuemax", "100");
    }
    const range = vertical
      ? container.scrollHeight - container.clientHeight
      : container.scrollWidth - container.clientWidth;
    const at = vertical ? container.scrollTop : container.scrollLeft;
    const now =
      range > 0
        ? Math.round((Math.min(Math.max(at, 0), range) / range) * 100)
        : 0;
    if (thumb.getAttribute("aria-valuenow") !== String(now))
      thumb.setAttribute("aria-valuenow", String(now));
  });
}
