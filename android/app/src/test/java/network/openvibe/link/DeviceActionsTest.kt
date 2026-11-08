package network.openvibe.link

import network.openvibe.link.data.ActionItem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class DeviceActionsTest {
    @Test fun cameraAndLocationAreNotOrdinaryApprovals() {
        val photo = ActionItem(service = "actor", id = "apr_camera", kind = "approval",
            summary = "device.camera · Read the sign before suggesting a route")
        val where = ActionItem(service = "actor", id = "apr_location", kind = "approval",
            summary = "device.location.precise · Find a nearby open pharmacy")
        assertEquals("camera", photo.deviceKind)
        assertEquals("Read the sign before suggesting a route", photo.deviceWhy)
        assertEquals("location", where.deviceKind)
        assertTrue(where.preciseRequested)
        assertFalse(photo.preciseRequested)
    }

    @Test fun onlyFlaggedActorItemsArePhoneNotifications() {
        val notice = ActionItem(service = "actor", id = "ntf_1", kind = "agent_task",
            summary = "notify · The report is ready")
        assertTrue(notice.isNotify)
        assertFalse(notice.copy(id = "tsk_1").isNotify)
        assertFalse(notice.copy(service = "chat").isNotify)
    }
}
