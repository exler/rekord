// screencapture_helper.swift
// Captures system audio via ScreenCaptureKit (macOS 13+) and writes raw
// float32-LE samples at 16 kHz mono to stdout.
//
// Build (done automatically by rekord on first run):
//   swiftc -O -o rekord-screencapture screencapture_helper.swift
//
// Permissions:
//   The calling process (or its parent terminal) must have Screen Recording
//   permission in System Settings > Privacy & Security > Screen Recording.

import Foundation
import ScreenCaptureKit
import CoreAudio

guard #available(macOS 13.0, *) else {
    fputs("error: system audio capture requires macOS 13.0 or later\n", stderr)
    exit(1)
}

// MARK: - Audio output handler

@available(macOS 13.0, *)
final class AudioOutput: NSObject, SCStreamOutput {
    func stream(
        _ stream: SCStream,
        didOutputSampleBuffer sampleBuffer: CMSampleBuffer,
        of type: SCStreamOutputType
    ) {
        guard type == .audio else { return }

        var blockBuffer: CMBlockBuffer?
        var audioBufferList = AudioBufferList()

        let status = CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(
            sampleBuffer,
            bufferListSizeNeededOut: nil,
            bufferListOut: &audioBufferList,
            bufferListSize: MemoryLayout<AudioBufferList>.size,
            blockBufferAllocator: nil,
            blockBufferMemoryAllocator: nil,
            flags: 0,
            blockBufferOut: &blockBuffer
        )
        guard status == noErr else { return }

        // channelCount=1 → mNumberBuffers == 1
        withUnsafePointer(to: audioBufferList.mBuffers) { ptr in
            let buf = ptr.pointee
            guard let data = buf.mData, buf.mDataByteSize > 0 else { return }
            let bytes = Data(bytes: data, count: Int(buf.mDataByteSize))
            FileHandle.standardOutput.write(bytes)
        }
    }
}

// MARK: - Stream delegate

@available(macOS 13.0, *)
final class StreamDelegate: NSObject, SCStreamDelegate {
    func stream(_ stream: SCStream, didStopWithError error: Error) {
        fputs("stream stopped: \(error.localizedDescription)\n", stderr)
        exit(1)
    }
}

// MARK: - Main

@available(macOS 13.0, *)
func run() async {
    do {
        let content = try await SCShareableContent.excludingDesktopWindows(
            false, onScreenWindowsOnly: false
        )
        guard let display = content.displays.first else {
            fputs("error: no display found\n", stderr)
            exit(1)
        }

        let filter = SCContentFilter(display: display, excludingWindows: [])

        let config = SCStreamConfiguration()
        config.capturesAudio = true
        config.sampleRate = 16000
        config.channelCount = 1
        config.excludesCurrentProcessAudio = false
        // Minimise video overhead (SCStream still requires a non-zero frame size)
        config.width = 2
        config.height = 2
        config.minimumFrameInterval = CMTime(value: 1, timescale: 1) // 1 fps

        let delegate = StreamDelegate()
        let stream = SCStream(filter: filter, configuration: config, delegate: delegate)

        let output = AudioOutput()
        try stream.addStreamOutput(output, type: .audio, sampleHandlerQueue: .global())
        try await stream.startCapture()

        // Signal readiness to the parent process and block until terminated.
        fputs("ready\n", stderr)

        // Park the task forever; the process is killed by the parent via context cancel.
        await withCheckedContinuation { (_: CheckedContinuation<Void, Never>) in }
    } catch {
        fputs("error: \(error.localizedDescription)\n", stderr)
        exit(1)
    }
}

Task { await run() }
RunLoop.main.run()
