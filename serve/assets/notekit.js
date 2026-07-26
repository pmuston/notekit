// notekit UI glue. Committed, embedded, no build step (harvest R9).
//
// Everything here is a live-only nicety and must degrade to nothing: with JavaScript
// off the page still renders every result, and a sortable table is simply a table. The
// durable form stands alone regardless.
(function () {
  "use strict";

  // --- Sortable tables (harvest V1) -----------------------------------------
  //
  // Sorting is client-side and never written anywhere: the table renders what was
  // persisted, in the order it was persisted, until a reader asks otherwise.

  function cellText(row, col) {
    var td = row.children[col];
    return td ? td.textContent.trim() : "";
  }

  // Compare numerically when both values look numeric, else lexically. Mixed columns
  // fall back to text, which is less surprising than an arbitrary ordering.
  function compare(a, b) {
    var na = parseFloat(a.replace(/[, ]/g, ""));
    var nb = parseFloat(b.replace(/[, ]/g, ""));
    var aNum = !isNaN(na) && /^[-+]?[\d,. ]+$/.test(a);
    var bNum = !isNaN(nb) && /^[-+]?[\d,. ]+$/.test(b);
    if (aNum && bNum) return na - nb;
    return a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" });
  }

  function sortTable(table, col, dir) {
    var tbody = table.tBodies[0];
    if (!tbody) return;
    var rows = Array.prototype.slice.call(tbody.rows);
    rows.sort(function (r1, r2) {
      var c = compare(cellText(r1, col), cellText(r2, col));
      return dir === "descending" ? -c : c;
    });
    rows.forEach(function (r) { tbody.appendChild(r); });
  }

  function bindTable(table) {
    if (table.dataset.nkBound === "true") return;
    table.dataset.nkBound = "true";

    Array.prototype.forEach.call(table.tHead ? table.tHead.rows[0].cells : [], function (th, col) {
      function activate() {
        var current = th.getAttribute("aria-sort");
        var dir = current === "ascending" ? "descending" : "ascending";
        Array.prototype.forEach.call(th.parentNode.cells, function (other) {
          other.removeAttribute("aria-sort");
        });
        th.setAttribute("aria-sort", dir);
        sortTable(table, col, dir);
      }
      th.addEventListener("click", activate);
      th.addEventListener("keydown", function (e) {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          activate();
        }
      });
    });
  }

  // --- Unsaved-changes indicator (harvest R5) -------------------------------
  //
  // A visible marker, plus a beforeunload guard so a reload cannot silently discard
  // an edit. The indicator is the point: prose is the one thing a user types by hand,
  // and losing it is unrecoverable in a way a re-runnable result is not.

  function dirtyCount() {
    return document.querySelectorAll('.nk-dirty[data-dirty="true"]').length;
  }

  function bindProse(textarea) {
    if (textarea.dataset.nkBound === "true") return;
    textarea.dataset.nkBound = "true";

    var initial = textarea.value;
    var indicator = textarea.closest(".nk-prose");
    indicator = indicator ? indicator.querySelector(".nk-dirty") : null;

    textarea.addEventListener("input", function () {
      if (!indicator) return;
      indicator.dataset.dirty = textarea.value !== initial ? "true" : "false";
    });
  }

  window.addEventListener("beforeunload", function (e) {
    if (dirtyCount() > 0) {
      e.preventDefault();
      // Browsers show their own wording; the return value only has to be truthy.
      e.returnValue = "";
      return "";
    }
  });

  // --- Transient flash messages --------------------------------------------

  function bindFlash(el) {
    setTimeout(function () { el.remove(); }, 2500);
  }

  // --- Binding, on load and after every HTMX swap ---------------------------
  //
  // HTMX replaces fragments, so binding has to run again on the new nodes rather than
  // once at load.

  function bindAll(root) {
    var scope = root || document;
    scope.querySelectorAll("table.nk-table[data-sortable]").forEach(bindTable);
    scope.querySelectorAll("textarea.nk-prose-edit").forEach(bindProse);
    scope.querySelectorAll(".nk-flash").forEach(bindFlash);
  }

  document.addEventListener("DOMContentLoaded", function () { bindAll(document); });
  document.body && document.addEventListener("htmx:afterSwap", function (e) {
    bindAll(e.detail && e.detail.target ? e.detail.target : document);
  });
})();
