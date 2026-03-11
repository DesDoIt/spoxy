import re
import sys

def main():
    try:
        with open("/tmp/web-player.js", "r") as f:
            content = f.read()
            
            print("--- Searching for TOTP Secret ---")
            idx = content.find('rLJ2oxaKL')
            if idx != -1:
                start = max(0, idx - 150)
                end = min(len(content), idx + 150)
                print(content[start:end])
            else:
                print("Secret not found")
                
            print("\n--- Searching for getTrack Hash ---")
            idx2 = content.find('612585ae')
            if idx2 != -1:
                start = max(0, idx2 - 150)
                end = min(len(content), idx2 + 150)
                print(content[start:end])
            else:
                print("getTrack hash not found")
                
            print("\n--- Searching for fetchPlaylist Hash ---")
            idx3 = content.find('9c53fb83')
            if idx3 != -1:
                start = max(0, idx3 - 150)
                end = min(len(content), idx3 + 150)
                print(content[start:end])
            else:
                print("fetchPlaylist hash not found")
                
    except Exception as e:
        print(f"Error: {e}")

if __name__ == "__main__":
    main()
