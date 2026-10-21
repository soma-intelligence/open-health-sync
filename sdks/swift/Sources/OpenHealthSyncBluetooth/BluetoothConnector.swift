// SPDX-License-Identifier: AGPL-3.0-only

#if canImport(CoreBluetooth)
import CoreBluetooth
import Foundation
import OpenHealthSync

/// Connects to standard Bluetooth health devices with CoreBluetooth and records what they
/// send in a `BluetoothRecorder`: heart rate straps, blood pressure cuffs, scales, glucose
/// meters, thermometers and pulse oximeters. Take the recorded values with `takePart()` and
/// upload them with `OHSClient.uploadBluetoothPart`.
///
/// For each device it reads the Device Information service, sets the device clock through
/// the Current Time service when the device allows it, subscribes to every measurement it
/// serves, and asks a glucose meter for the records after the last one it recorded. It
/// reconnects when a device comes back. Events arrive on the connector's own queue.
///
/// iOS needs `NSBluetoothAlwaysUsageDescription` in the app's Info.plist. Cuffs and meters
/// usually need pairing: iOS shows its prompt on the first subscription.
public final class BluetoothConnector: NSObject, @unchecked Sendable {
    public enum Event: Sendable {
        case connected(name: String)
        /// A value as received (measurements, battery level), for a live view.
        case value(name: String, characteristic: String, value: Data)
        /// Something the user may need to act on, such as pairing.
        case note(name: String, text: String)
        case disconnected(name: String)
    }

    private final class Link {
        let peripheral: CBPeripheral
        var device: BluetoothDevice
        let session = BluetoothRecorder.newSession()
        var servicesLeft = 0
        var readsLeft = 0
        var started = false
        var toSubscribe: [CBCharacteristic] = []
        var racp: CBCharacteristic?
        var currentTime: CBCharacteristic?
        var hasGlucose = false

        init(_ peripheral: CBPeripheral, name: String) {
            self.peripheral = peripheral
            self.device = BluetoothDevice(name: BluetoothGATT.publicName(name))
        }
    }

    private let queue = DispatchQueue(label: "org.openhealthsync.bluetooth")
    private let names: [String]
    private let defaults: UserDefaults
    private let onEvent: @Sendable (Event) -> Void
    private var central: CBCentralManager!
    private var recorder: BluetoothRecorder
    private var links: [UUID: Link] = [:]
    private var wanted = false

    /// - Parameters:
    ///   - deviceNames: name fragments to connect to, ignoring case; empty connects to any
    ///     device advertising a health service.
    ///   - deviceUser: this person's user index (0 to 254) on a shared scale or cuff.
    ///   - defaults: where the last glucose sequence number of each meter is kept.
    ///   - onEvent: called on the connector's queue.
    public init(
        deviceNames: [String] = [], deviceUser: Int? = nil, defaults: UserDefaults = .standard,
        onEvent: @escaping @Sendable (Event) -> Void = { _ in }
    ) throws {
        self.names = deviceNames.map { $0.lowercased() }
        self.recorder = try BluetoothRecorder(deviceUser: deviceUser)
        self.defaults = defaults
        self.onEvent = onEvent
        super.init()
        self.central = CBCentralManager(delegate: self, queue: queue)
    }

    /// Starts scanning (as soon as Bluetooth is on) and connecting.
    public func start() {
        queue.async {
            self.wanted = true
            self.scan()
        }
    }

    /// Stops scanning and disconnects every device.
    public func stop() {
        queue.async {
            self.wanted = false
            if self.central.state == .poweredOn {
                self.central.stopScan()
            }
            for link in self.links.values {
                self.central.cancelPeripheralConnection(link.peripheral)
            }
            self.links.removeAll()
        }
    }

    /// The values recorded since the last call, as one part to upload; nil when there are
    /// none. Keep the part until the server stores it.
    public func takePart(batch: String = BluetoothRecorder.newBatchId()) throws -> BluetoothPart? {
        return try queue.sync { try recorder.makePart(batch: batch) }
    }

    private func scan() {
        guard wanted, central.state == .poweredOn else { return }
        // A name filter also finds devices that do not advertise their service.
        let services = names.isEmpty ? BluetoothGATT.healthServices.map { CBUUID(string: $0) } : nil
        central.scanForPeripherals(withServices: services, options: nil)
    }

    private func short(_ uuid: CBUUID) -> String {
        return BluetoothGATT.shortForm(uuid.uuidString) ?? ""
    }

    private func glucoseKey(_ id: UUID) -> String {
        return "org.openhealthsync.bluetooth.glucose." + id.uuidString
    }

    /// Subscribes once Device Information is read, so every line carries it.
    private func startIfReady(_ link: Link) {
        guard !link.started, link.servicesLeft == 0, link.readsLeft == 0 else { return }
        link.started = true
        let p = link.peripheral
        if let ct = link.currentTime {
            p.writeValue(BluetoothGATT.currentTimeValue(Date()), for: ct, type: .withResponse)
        }
        onEvent(.connected(name: link.device.name))
        for c in link.toSubscribe {
            p.setNotifyValue(true, for: c)
        }
        if link.hasGlucose, let racp = link.racp {
            p.setNotifyValue(true, for: racp)
        }
    }
}

extension BluetoothConnector: CBCentralManagerDelegate {
    public func centralManagerDidUpdateState(_ central: CBCentralManager) {
        scan()
    }

    public func centralManager(
        _ central: CBCentralManager, didDiscover peripheral: CBPeripheral,
        advertisementData: [String: Any], rssi RSSI: NSNumber
    ) {
        guard wanted, links[peripheral.identifier] == nil else { return }
        let name = peripheral.name ?? advertisementData[CBAdvertisementDataLocalNameKey] as? String ?? ""
        let services = (advertisementData[CBAdvertisementDataServiceUUIDsKey] as? [CBUUID] ?? []).map { short($0) }
        let health = BluetoothGATT.healthServices.map { $0.lowercased() }
        let matches = names.isEmpty
            ? services.contains { health.contains($0) }
            : names.contains { name.lowercased().contains($0) }
        guard matches else { return }
        links[peripheral.identifier] = Link(peripheral, name: name.isEmpty ? "unnamed device" : name)
        central.connect(peripheral, options: nil)
    }

    public func centralManager(_ central: CBCentralManager, didConnect peripheral: CBPeripheral) {
        peripheral.delegate = self
        peripheral.discoverServices(nil)
    }

    public func centralManager(_ central: CBCentralManager, didFailToConnect peripheral: CBPeripheral, error: Error?) {
        // Ask again: CoreBluetooth completes the request when the device is back in range.
        if wanted, links[peripheral.identifier] != nil {
            central.connect(peripheral, options: nil)
        }
    }

    public func centralManager(
        _ central: CBCentralManager, didDisconnectPeripheral peripheral: CBPeripheral, error: Error?
    ) {
        guard let old = links[peripheral.identifier] else { return }
        if old.started {
            onEvent(.disconnected(name: old.device.name))
        }
        guard wanted else { return }
        // A new link: a new session and a fresh read of Device Information.
        let link = Link(peripheral, name: old.device.name)
        links[peripheral.identifier] = link
        central.connect(peripheral, options: nil)
    }
}

extension BluetoothConnector: CBPeripheralDelegate {
    public func peripheral(_ peripheral: CBPeripheral, didDiscoverServices error: Error?) {
        guard let link = links[peripheral.identifier] else { return }
        let services = peripheral.services ?? []
        link.servicesLeft = services.count
        for s in services {
            peripheral.discoverCharacteristics(nil, for: s)
        }
        startIfReady(link)
    }

    public func peripheral(
        _ peripheral: CBPeripheral, didDiscoverCharacteristicsFor service: CBService, error: Error?
    ) {
        guard let link = links[peripheral.identifier] else { return }
        let notifiable: CBCharacteristicProperties = [.notify, .indicate]
        for c in service.characteristics ?? [] {
            let id = short(c.uuid)
            switch id {
            case BluetoothGATT.manufacturerName.lowercased(), BluetoothGATT.modelNumber.lowercased(),
                BluetoothGATT.firmwareRevision.lowercased():
                if c.properties.contains(.read) {
                    link.readsLeft += 1
                    peripheral.readValue(for: c)
                }
            case BluetoothGATT.batteryLevel.lowercased():
                if c.properties.contains(.read) {
                    peripheral.readValue(for: c)
                }
                if c.properties.contains(.notify) {
                    link.toSubscribe.append(c)
                }
            case BluetoothGATT.currentTime.lowercased():
                if c.properties.contains(.write) {
                    link.currentTime = c
                }
            case BluetoothGATT.recordAccessControlPoint.lowercased():
                link.racp = c
            default:
                if BluetoothGATT.isMeasurement(id), !c.properties.intersection(notifiable).isEmpty {
                    link.toSubscribe.append(c)
                    if id == BluetoothGATT.glucoseMeasurement.lowercased() {
                        link.hasGlucose = true
                    }
                }
            }
        }
        link.servicesLeft -= 1
        startIfReady(link)
    }

    public func peripheral(
        _ peripheral: CBPeripheral, didUpdateNotificationStateFor characteristic: CBCharacteristic, error: Error?
    ) {
        guard let link = links[peripheral.identifier] else { return }
        let id = short(characteristic.uuid)
        if let error = error {
            if BluetoothGATT.isMeasurement(id) {
                onEvent(.note(name: link.device.name, text: "cannot subscribe (\(error.localizedDescription)): pair the device first"))
            }
            return
        }
        // Glucose: ask for the records after the last one recorded, all of them the first time.
        if id == BluetoothGATT.recordAccessControlPoint.lowercased(), characteristic.isNotifying {
            let stored = defaults.object(forKey: glucoseKey(peripheral.identifier)) as? Int
            let last = stored.flatMap { UInt16(exactly: $0) }
            peripheral.writeValue(BluetoothGATT.reportStoredRecords(after: last), for: characteristic, type: .withResponse)
        }
    }

    public func peripheral(_ peripheral: CBPeripheral, didUpdateValueFor characteristic: CBCharacteristic, error: Error?) {
        guard let link = links[peripheral.identifier] else { return }
        let id = short(characteristic.uuid)
        let value = characteristic.value ?? Data()
        switch id {
        case BluetoothGATT.manufacturerName.lowercased(), BluetoothGATT.modelNumber.lowercased(),
            BluetoothGATT.firmwareRevision.lowercased():
            if error == nil {
                let text = BluetoothGATT.deviceText(value)
                switch id {
                case BluetoothGATT.manufacturerName.lowercased(): link.device.manufacturer = text
                case BluetoothGATT.modelNumber.lowercased(): link.device.model = text
                default: link.device.firmware = text
                }
            }
            if !link.started {
                link.readsLeft -= 1
                startIfReady(link)
            }
            return
        default:
            break
        }
        guard error == nil, !value.isEmpty else { return }
        if BluetoothGATT.isMeasurement(id) {
            if (try? recorder.record(characteristic: id, value: value, session: link.session, device: link.device)) == true,
               id == BluetoothGATT.glucoseMeasurement.lowercased(),
               let seq = BluetoothGATT.glucoseSequenceNumber(value) {
                defaults.set(Int(seq), forKey: glucoseKey(peripheral.identifier))
            }
        }
        if BluetoothGATT.isMeasurement(id) || id == BluetoothGATT.batteryLevel.lowercased() {
            onEvent(.value(name: link.device.name, characteristic: id, value: value))
        }
    }
}
#endif
